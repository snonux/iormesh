#!/usr/bin/env bash
# S2 spike (throwaway, task 6b4): two "hosts" with a NAT box between them.
#
#   s2hostA 10.0.1.1 --veth-- 10.0.1.254 s2nat 10.0.2.254 --veth-- 10.0.2.2 s2hostB
#
# The client in s2hostA connects to the service VIP 10.0.1.254:8080; s2nat
# DNATs it to the server at 10.0.2.2:8080. Pass "dnat-snat" also SNATs to
# 10.0.2.254 with a random port, so no address or port of the connection is
# the same on both ends. For each pass the script runs isn.bt (ISN state on
# both ends, conntrack inserts in s2nat), tcpdump on both ends (ground truth
# for the ISNs), and the workload (plain, TIME_WAIT-reuse, RST-reuse and TFO
# connections),
# dumps /proc/net/nf_conntrack from s2nat, then runs join.py over it all.
#
# Usage: sudo -n spikes/s2/rig.sh OUTDIR
# Everything it creates lives in the three namespaces, which the EXIT trap
# deletes; the host's own interfaces, routes and firewall are not touched.
set -euo pipefail

declare -r NS_A=s2hostA NS_N=s2nat NS_B=s2hostB
declare -r VIP=10.0.1.254 SERVER=10.0.2.2 PORT=8080
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
declare -r HERE
declare -a BG_PIDS=()

log() {
    echo "rig: $*" >&2
}

rig::down() {
    local pid
    for pid in "${BG_PIDS[@]}"; do
        kill "$pid" 2>/dev/null || :
    done
    wait 2>/dev/null || :
    local ns
    for ns in "$NS_A" "$NS_N" "$NS_B"; do
        ip netns del "$ns" 2>/dev/null || :
    done
}

# rig::up creates the namespaces and veth pairs directly inside them, so no
# interface ever exists in the host namespace.
rig::up() {
    local ns
    for ns in "$NS_A" "$NS_N" "$NS_B"; do
        ip netns add "$ns"
        ip -n "$ns" link set lo up
    done
    ip link add s2a0 netns "$NS_A" type veth peer name s2na netns "$NS_N"
    ip link add s2b0 netns "$NS_B" type veth peer name s2nb netns "$NS_N"
    ip -n "$NS_A" addr add 10.0.1.1/24 dev s2a0
    ip -n "$NS_N" addr add 10.0.1.254/24 dev s2na
    ip -n "$NS_N" addr add 10.0.2.254/24 dev s2nb
    ip -n "$NS_B" addr add 10.0.2.2/24 dev s2b0
    ip -n "$NS_A" link set s2a0 up
    ip -n "$NS_N" link set s2na up
    ip -n "$NS_N" link set s2nb up
    ip -n "$NS_B" link set s2b0 up
    ip -n "$NS_A" route add default via 10.0.1.254
    ip -n "$NS_B" route add default via 10.0.2.254
    ip netns exec "$NS_N" sysctl -qw net.ipv4.ip_forward=1
    # TFO: client bit (1) on A, server bit (2) on B; both per namespace.
    ip netns exec "$NS_A" sysctl -qw net.ipv4.tcp_fastopen=3
    ip netns exec "$NS_B" sysctl -qw net.ipv4.tcp_fastopen=3
}

# rig::nat installs the NAT table for one pass (dnat or dnat-snat).
rig::nat() {
    local -r pass="$1"; shift
    local snat=""
    if [[ "$pass" == dnat-snat ]]; then
        snat="oifname s2nb ip daddr $SERVER tcp dport $PORT"
        snat+=" snat to 10.0.2.254:40000-40999 random"
    fi
    ip netns exec "$NS_N" nft -f - <<EOF
table ip s2 {
    chain pre {
        type nat hook prerouting priority dstnat;
        ip daddr $VIP tcp dport $PORT dnat to $SERVER:$PORT
    }
    chain post {
        type nat hook postrouting priority srcnat;
        $snat
    }
}
EOF
}

_netns_inode() {
    stat -L -c %i "/run/netns/$1"
}

# _wait_for waits up to 30 s for a pattern in a file.
_wait_for() {
    local -r file="$1"; shift
    local -r pattern="$1"; shift
    local -i i
    for ((i = 0; i < 300; i++)); do
        if grep -q "$pattern" "$file" 2>/dev/null; then
            return 0
        fi
        sleep 0.1
    done
    log "timeout waiting for '$pattern' in $file"
    return 1
}

rig::capture_start() {
    local -r out="$1"; shift
    bpftrace "$HERE/isn.bt" "$(_netns_inode "$NS_A")" \
        "$(_netns_inode "$NS_B")" "$(_netns_inode "$NS_N")" "$PORT" \
        >"$out/isn.log" 2>&1 &
    BG_PIDS+=($!)
    # The server must start only after the probes are attached: an fexit
    # trampoline does not fire for a call already in progress, and the
    # server blocks inside inet_csk_accept at once (the first run lost its
    # first accept that way).
    _wait_for "$out/isn.log" Attached
    ip netns exec "$NS_A" tcpdump -U -n -i s2a0 -w "$out/hostA.pcap" \
        "tcp port $PORT" 2>/dev/null &
    BG_PIDS+=($!)
    ip netns exec "$NS_B" tcpdump -U -n -i s2b0 -w "$out/hostB.pcap" \
        "tcp port $PORT" 2>/dev/null &
    BG_PIDS+=($!)
    ip netns exec "$NS_B" python3 -u "$HERE/workload.py" server "$PORT" \
        >"$out/server.log" 2>&1 &
    BG_PIDS+=($!)
    _wait_for "$out/server.log" ready
    sleep 1 # tcpdump has no ready signal
}

rig::capture_stop() {
    local -r out="$1"; shift
    sleep 1
    ip netns exec "$NS_N" cat /proc/net/nf_conntrack >"$out/conntrack.txt"
    local pid
    for pid in "${BG_PIDS[@]}"; do
        kill -TERM "$pid" 2>/dev/null || :
    done
    wait 2>/dev/null || :
    BG_PIDS=()
    local side
    for side in hostA hostB; do
        tcpdump -r "$out/$side.pcap" -nn -S -tt \
            'tcp[tcpflags] & tcp-syn != 0' >"$out/$side.syn.txt" 2>/dev/null
        tcpdump -r "$out/$side.pcap" -nn -S -tt \
            'tcp[tcpflags] & tcp-rst != 0' >"$out/$side.rst.txt" 2>/dev/null
    done
}

rig::pass() {
    local -r pass="$1"; shift
    local -r out="$1/$pass"; shift
    mkdir -p "$out"
    log "pass $pass -> $out"
    rig::up
    rig::nat "$pass"
    rig::capture_start "$out"
    local mode
    for mode in plain:12 reuse-tw:6 reuse-rst:6 tfo:6; do
        ip netns exec "$NS_A" python3 "$HERE/workload.py" client \
            "$VIP" "$PORT" "${mode%%:*}" "${mode##*:}"
    done
    rig::capture_stop "$out"
    rig::down
    python3 "$HERE/join.py" "$out" | tee "$out/join.txt"
}

main() {
    local -r out="${1:?usage: rig.sh OUTDIR}"; shift
    trap rig::down EXIT
    rig::down # leftovers from an aborted run
    mkdir -p "$out"
    local pass
    for pass in dnat dnat-snat; do
        rig::pass "$pass" "$out"
    done
    if [[ -n "${SUDO_UID:-}" ]]; then
        chown -R "$SUDO_UID:${SUDO_GID:-$SUDO_UID}" "$out"
    fi
}

main "$@"
