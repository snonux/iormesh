#!/usr/bin/env bash
# A throwaway single-node k3s on an el9 host with SELinux enforcing, for
# M7's acceptance against an existing cluster (task zg4, docs/testing.md
# "M7 on an existing cluster"). Run as root on the node:
#
#   el9-k3s.sh up        install k3s (its SELinux policy, k3s-selinux,
#                        comes with it) with containerd's SELinux support
#                        on, no bundled Traefik (the harness installs its
#                        own), and a mirror for localhost:5001, the
#                        laptop's registry reached through `ssh -R`
#   el9-k3s.sh proxy M   restart k3s with kube-proxy in mode M
#                        (iptables or nftables)
#   el9-k3s.sh down      uninstall k3s, its images and data, and the
#                        k3s-selinux package and repository `up` added
#   el9-k3s.sh status    SELinux mode, k3s state, disk use
#
# k3s embeds kube-proxy, so its mode is a k3s flag, not kind's configmap.
# `down` touches nothing `up` did not add: /opt/iormesh stays as it is.
set -euo pipefail

declare -r K3S_CHANNEL="${K3S_CHANNEL:-v1.35}"
declare -r CONFIG=/etc/rancher/k3s/config.yaml
declare -r REGISTRIES=/etc/rancher/k3s/registries.yaml
declare -r STATE=/root/.iormesh-k3s-state

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

up() {
    [[ "$(getenforce)" == Enforcing ]] || _die "SELinux is not enforcing"
    [[ -e /usr/local/bin/k3s ]] && _die "k3s is installed already"
    # Record what `down` may remove: only what was not there before.
    {
        echo "container_selinux_before=$(rpm -q container-selinux \
            > /dev/null && echo yes || echo no)"
        echo "repo_before=$([[ -e /etc/yum.repos.d/rancher-k3s-common.repo ]] \
            && echo yes || echo no)"
    } > "$STATE"
    _write_config iptables
    curl -sfL https://get.k3s.io \
        | INSTALL_K3S_CHANNEL="$K3S_CHANNEL" sh -s - server
    rpm -q k3s-selinux container-selinux
    _wait_ready
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
    if [[ -x /usr/local/bin/k3s-killall.sh ]]; then
        /usr/local/bin/k3s-killall.sh || true
    fi
    if [[ -x /usr/local/bin/k3s-uninstall.sh ]]; then
        /usr/local/bin/k3s-uninstall.sh
    fi
    rm -rf /etc/rancher/k3s
    # Without a state file nothing counts as ours: both are kept.
    local container_selinux_before=yes repo_before=yes key value
    if [[ -r "$STATE" ]]; then
        while IFS='=' read -r key value; do
            case "$key" in
                container_selinux_before) container_selinux_before="$value" ;;
                repo_before) repo_before="$value" ;;
            esac
        done < "$STATE"
    fi
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
    if [[ -e /usr/local/bin/k3s ]]; then
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
