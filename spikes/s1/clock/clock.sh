#!/usr/bin/env bash
# Throwaway (S1, task xa4): runs ClockProbe under clock.bt and analyzes.
# Usage: clock.sh <JAVA_HOME> <N> <outdir>
set -euo pipefail

readonly jh="${1:?usage: clock.sh JAVA_HOME N OUTDIR}"
readonly n="${2:?N}"
readonly out="${3:?outdir}"
here="$(cd "$(dirname "$0")" && pwd)"
readonly here

mkdir -p "$out"
rm -f "$out/gate"
"$jh/bin/java" "$here/ClockProbe.java" "$n" "$out/gate" "$out/brackets.txt" \
  > "$out/probe.stdout" &
readonly jpid="$!"

# cleanup: never leave a root bpftrace or the gated JVM behind.
cleanup() {
  kill "$jpid" 2>/dev/null || true
  sudo -n pkill -INT -f "bpftrace $here/clock.bt" || true
  wait || true
}
trap cleanup EXIT

for _ in $(seq 100); do
  [[ -s "$out/probe.stdout" ]] && break
  sleep 0.1
done
pid="$(head -1 "$out/probe.stdout")"
# shellcheck disable=SC2024  # the log stays owned by the invoking user
sudo -n bpftrace "$here/clock.bt" "$pid" > "$out/clock.log" 2> "$out/bt.err" &
for _ in $(seq 300); do
  grep -q '^READY' "$out/clock.log" 2>/dev/null && break
  sleep 0.1
done
touch "$out/gate"
wait "$jpid" || true
sleep 0.5
sudo -n pkill -INT -f "bpftrace $here/clock.bt" || true
wait || true
python3 "$here/clock.py" "$out"
