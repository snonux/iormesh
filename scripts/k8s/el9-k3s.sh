#!/usr/bin/env bash
# A throwaway single-node k3s on an el9 host with SELinux enforcing, for
# M7's acceptance against an existing cluster (task zg4, docs/testing.md
# "M7 on an existing cluster"). Run as root on the node:
#
#   el9-k3s.sh up        install k3s $K3S_VERSION (its SELinux policy,
#                        k3s-selinux, comes with it) with containerd's
#                        SELinux support on, no bundled Traefik (the
#                        harness installs its own) and no metrics-server
#                        (on iom1 it never became Ready, and its stale API
#                        stalled every namespace deletion, task zg4), and
#                        a mirror for localhost:5001, the laptop's
#                        registry reached through `ssh -R`
#   el9-k3s.sh proxy M   restart k3s with kube-proxy in mode M
#                        (iptables or nftables)
#   el9-k3s.sh down      uninstall k3s, its images and data, and the
#                        k3s-selinux package, repository and firewalld
#                        sources `up` added
#   el9-k3s.sh status    SELinux mode, k3s state, disk use
#
# k3s embeds kube-proxy, so its mode is a k3s flag, not kind's configmap.
# `down` touches nothing `up` did not add: it refuses to run without the
# state file `up` writes (a host where `up` did not install k3s may be a
# real node, task ni4), and /opt/iormesh stays as it is.
set -euo pipefail

# The k3s release and the sha256 of its install.sh, fetched from that
# release's tag rather than piping get.k3s.io's moving script into a root
# shell (task ni4). The installer then checks the k3s binary against the
# release's sha256sum file. To bump: pick the release, set K3S_VERSION,
# and set K3S_INSTALLER_SHA256 to the output of
#   curl -sfL https://raw.githubusercontent.com/k3s-io/k3s/<tag>/install.sh \
#     | sha256sum
# with the tag's `+` written as %2B (docs/testing.md).
declare -r K3S_VERSION=v1.35.10+k3s1
declare -r K3S_INSTALLER_SHA256=8598e002e61d658fed7b7542fc6d2c66d8da6eae69e088830105d2ee1ffb6d91
declare -r CONFIG=/etc/rancher/k3s/config.yaml
declare -r REGISTRIES=/etc/rancher/k3s/registries.yaml
# The state file and k3s's bin directory are overridable only so that
# scripts/k8s/el9k3s_test.go can run `down` against fakes; on a node
# leave both unset.
declare -r STATE="${IORMESH_K3S_STATE:-/root/.iormesh-k3s-state}"
declare -r BIN="${IORMESH_K3S_BIN:-/usr/local/bin}"
# The pod and Service CIDRs firewalld must trust (see _trust_cidrs).
declare -ra CIDRS=(10.42.0.0/16 10.43.0.0/16)

_die() {
    echo "${0##*/}: $*" >&2
    exit 1
}

# _write_config writes k3s's config with kube-proxy in mode $1.
_write_config() {
    local -r mode="$1"
    mkdir -p "${CONFIG%/*}"
    cat > "$CONFIG" <<EOF
selinux: true
disable:
  - traefik
  - metrics-server
write-kubeconfig-mode: "0600"
kube-proxy-arg:
  - proxy-mode=$mode
EOF
    cat > "$REGISTRIES" <<'EOF'
mirrors:
  "localhost:5001":
    endpoint:
      - "http://localhost:5001"
EOF
}

# _wait_ready waits, bounded, for the node to report Ready.
_wait_ready() {
    local -i i
    for (( i = 0; i < 60; i++ )); do
        if k3s kubectl get nodes 2> /dev/null | grep -q ' Ready '; then
            k3s kubectl get nodes -o wide
            return 0
        fi
        sleep 5
    done
    _die "node not Ready after 5 minutes"
}

# _install_k3s downloads $K3S_VERSION's install.sh, refuses it unless its
# sha256 is $K3S_INSTALLER_SHA256, and runs it for that version.
_install_k3s() {
    local installer
    installer="$(mktemp)"
    if ! curl -sfL -o "$installer" \
        "https://raw.githubusercontent.com/k3s-io/k3s/${K3S_VERSION/+/%2B}/install.sh" \
        || ! echo "$K3S_INSTALLER_SHA256  $installer" | sha256sum -c --quiet -; then
        rm -f "$installer"
        _die "k3s installer for $K3S_VERSION missing or not sha256 $K3S_INSTALLER_SHA256"
    fi
    if ! INSTALL_K3S_VERSION="$K3S_VERSION" sh "$installer" server; then
        rm -f "$installer"
        _die "k3s installer failed"
    fi
    rm -f "$installer"
}

up() {
    [[ "$(getenforce)" == Enforcing ]] || _die "SELinux is not enforcing"
    [[ -e "$BIN/k3s" ]] && _die "k3s is installed already"
    # Record what `down` may remove: only what was not there before.
    # _trust_cidrs appends firewalld_added once it has run; a state file
    # without it (up failed earlier) leaves firewalld alone in `down`.
    {
        echo "container_selinux_before=$(rpm -q container-selinux \
            > /dev/null && echo yes || echo no)"
        echo "repo_before=$([[ -e /etc/yum.repos.d/rancher-k3s-common.repo ]] \
            && echo yes || echo no)"
    } > "$STATE"
    _write_config iptables
    _install_k3s
    rpm -q k3s-selinux container-selinux
    _trust_cidrs
    _wait_ready
}

# _trust_cidrs adds each of k3s's pod and Service CIDRs that firewalld's
# trusted zone lacks, at runtime only (a reboot forgets it), and records
# the ones it added as firewalld_added in $STATE, so `down` removes only
# those. el9's firewalld rejects forwarded pod traffic otherwise
# (`reject with icmpx admin-prohibited`): on iom1 Traefik's pod could not
# reach the backend pod (task zg4); k3s documents the same two sources.
#
# Each add appends the full list so far (`down` reads the last line), so
# a failure part way through still leaves the sources added before it on
# record. A failed add is tolerated, as before: the node then shows the
# reject in its traffic, not here.
_trust_cidrs() {
    local cidr added=()
    echo "firewalld_added=" >> "$STATE"
    systemctl is-active -q firewalld || return 0
    for cidr in "${CIDRS[@]}"; do
        if ! firewall-cmd --zone=trusted --query-source="$cidr" \
            > /dev/null 2>&1 \
            && firewall-cmd --zone=trusted --add-source="$cidr"; then
            added+=("$cidr")
            echo "firewalld_added=${added[*]}" >> "$STATE"
        fi
    done
}

proxy() {
    local -r mode="${1:-}"
    case "$mode" in
        iptables|nftables) ;;
        *) _die "proxy takes iptables or nftables" ;;
    esac
    _write_config "$mode"
    systemctl restart k3s
    _wait_ready
}

down() {
    # Without the state file `up` did not install k3s here: this may be a
    # real node, whose k3s, datastore and packages are not ours to remove.
    [[ -r "$STATE" ]] || _die "no $STATE: up did not install k3s here, refusing"
    # A key the file lacks counts as not ours: kept. A repeated key
    # (firewalld_added) takes its last value.
    local container_selinux_before=yes repo_before=yes firewalld_added=
    local key value cidr
    while IFS='=' read -r key value; do
        case "$key" in
            container_selinux_before) container_selinux_before="$value" ;;
            repo_before) repo_before="$value" ;;
            firewalld_added) firewalld_added="$value" ;;
        esac
    done < "$STATE"
    if [[ -x "$BIN/k3s-killall.sh" ]]; then
        "$BIN/k3s-killall.sh" || true
    fi
    if [[ -x "$BIN/k3s-uninstall.sh" ]]; then
        "$BIN/k3s-uninstall.sh"
    fi
    # k3s-uninstall.sh leaves the node password and the empty parents;
    # on this throwaway node both trees are k3s's alone (no RKE2).
    rm -rf /etc/rancher /var/lib/rancher
    # Runtime sources a reboot already dropped make firewall-cmd fail;
    # that is the state we want.
    for cidr in $firewalld_added; do
        firewall-cmd --zone=trusted "--remove-source=$cidr" || true
    done
    # k3s-uninstall.sh removes k3s-selinux itself on el; this is the
    # rest, against the state `up` recorded.
    if rpm -q k3s-selinux > /dev/null 2>&1; then
        dnf -y remove k3s-selinux
    fi
    if [[ "$container_selinux_before" == no ]] \
        && rpm -q container-selinux > /dev/null 2>&1; then
        dnf -y remove container-selinux
    fi
    if [[ "$repo_before" == no ]]; then
        rm -f /etc/yum.repos.d/rancher-k3s-common.repo
    fi
    rm -f "$STATE"
    status
}

status() {
    echo "SELinux: $(getenforce)"
    if [[ -e "$BIN/k3s" ]]; then
        echo "k3s: installed, $(systemctl is-active k3s || true)"
    else
        echo "k3s: not installed"
    fi
    rpm -q k3s-selinux container-selinux || true
    df -h / /var
}

main() {
    local -r cmd="${1:-}"
    shift || true
    case "$cmd" in
        up|down|status) "$cmd" ;;
        proxy) proxy "$@" ;;
        *) _die "usage: ${0##*/} up|proxy iptables|nftables|down|status" ;;
    esac
}

main "$@"
