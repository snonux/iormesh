#!/usr/bin/env bash
# Throwaway (S1, task wa4): run one M0 scenario under join.bt and keep
# the raw logs. Each JVM gets its own bpftrace, attached while the JVM
# waits at the workload's startup gate, so no scenario syscall predates
# the probes. tcp-loopback runs two JVMs: the server first (its
# "listening <port>" line comes after the gate), then the client.
#
# Usage: [HOOKS=hooks-all.txt] run.sh <JAVA_HOME> <file-basic|tcp-loopback> <outdir>
# HOOKS defaults to the curated hooks-v1.txt. JAVA_OPTS is passed to each
# JVM with "{out}" replaced by <outdir>/<jvm name> — e.g.
#   JAVA_OPTS='-javaagent:B/agent.jar=B/bridge.jar,{out}'
# so each JVM's agent writes {out}.hooks and {out}.recs (agent/).
set -euo pipefail

readonly jh="${1:?usage: run.sh JAVA_HOME SCENARIO OUTDIR}"
readonly scenario="${2:?scenario}"
readonly out="${3:?outdir}"
here="$(cd "$(dirname "$0")" && pwd)"
readonly here
readonly jar="$here/../../workload.jar"
readonly hooks="${HOOKS:-$here/hooks-v1.txt}"

# probe_list <uprobe|uretprobe>: the hook set as a comma-separated list of
# bpftrace attach points with absolute library paths.
probe_list() {
  grep -v -e '^#' -e '^$' "$hooks" |
    awk -v kind="$1" -v lib="$jh/lib" '{ print kind ":" lib "/" $1 ":" $2 }' |
    paste -sd, -
}

mkdir -p "$out"
# PIDs of the JVMs this script started; cleanup kills any still alive.
jvm_pids=()
cp "$hooks" "$out/hooks.txt"
python3 - "$here/join.bt.in" "$(probe_list uprobe)" "$(probe_list uretprobe)" \
  > "$out/join.bt" <<'PY'
import sys
src, entry, ret = sys.argv[1:]
print(open(src).read().replace("@ENTRY@", entry).replace("@RETURN@", ret), end="")
PY

# start_jvm <name> <args...>: launches the workload gated and sets
# started_pid (not via $(...): a subshell could not record jvm_pids).
start_jvm() {
  local name="$1"; shift
  rm -f "$out/$name.gate"
  local opts="${JAVA_OPTS:-}"
  opts="${opts//\{out\}/$out/$name}"
  # $opts unquoted on purpose: it is a list of JVM flags.
  # shellcheck disable=SC2086
  IORMESH_WORKLOAD_STARTUP_FILE="$out/$name.gate" \
    "$jh/bin/java" $opts -jar "$jar" "$@" > "$out/$name.stdout" 2> "$out/$name.stderr" &
  jvm_pids+=("$!")
  for _ in $(seq 100); do
    [[ -s "$out/$name.stdout" ]] && break
    sleep 0.1
  done
  started_pid="$(head -1 "$out/$name.stdout")"
  if [[ ! "$started_pid" =~ ^[0-9]+$ ]]; then
    echo "no PID from the $name JVM (see $out/$name.stderr)" >&2
    return 1
  fi
}

# trace <name> <pid>: attaches join.bt to pid and returns once it is live.
trace() {
  local name="$1" pid="$2"
  # Redirects as the invoking user on purpose: the logs stay ours.
  # shellcheck disable=SC2024
  sudo -n bpftrace "$out/join.bt" "$pid" > "$out/$name.log" 2> "$out/$name.bt.err" &
  for _ in $(seq 300); do
    grep -q '^READY' "$out/$name.log" 2>/dev/null && return 0
    sleep 0.1
  done
  echo "bpftrace did not attach for $name" >&2
  return 1
}

# stop_trace: ends every bpftrace this script started.
stop_trace() {
  sudo -n pkill -INT -f "bpftrace $out/join.bt" || true
}

# cleanup: EXIT trap — kills leftover JVMs, stops bpftrace, reaps children.
cleanup() {
  local pid
  for pid in "${jvm_pids[@]}"; do
    kill "$pid" 2>/dev/null || true
  done
  stop_trace
  wait || true
}
# Always tear down: a failed step must never leave a root bpftrace with
# hundreds of uprobes on the shared JDK libraries, or a gated JVM. Set
# before anything is started (the render above starts nothing).
trap cleanup EXIT

run_file_basic() {
  start_jvm solo file-basic --dir "$out"
  local pid="$started_pid"
  trace solo "$pid"
  touch "$out/solo.gate"
  wait_pid "$pid"
}

run_tcp_loopback() {
  local spid cpid port=""
  start_jvm server tcp-loopback --role server --dir "$out"
  spid="$started_pid"
  trace server "$spid"
  touch "$out/server.gate"
  for _ in $(seq 100); do
    port="$(awk '/^listening/ {print $2}' "$out/server.stdout")"
    [[ -n "$port" ]] && break
    sleep 0.1
  done
  if [[ -z "$port" ]]; then
    echo "server never printed its port" >&2
    return 1
  fi
  start_jvm client tcp-loopback --role client --port "$port" --dir "$out"
  cpid="$started_pid"
  trace client "$cpid"
  touch "$out/client.gate"
  wait_pid "$cpid"
  wait_pid "$spid"
}

# wait_pid <pid>: waits for a JVM to exit, failing after 60 s (the
# scenarios take well under a second once the gate opens).
wait_pid() {
  local i
  for ((i = 0; i < 600; i++)); do
    kill -0 "$1" 2>/dev/null || return 0
    sleep 0.1
  done
  echo "JVM $1 still running after 60 s" >&2
  return 1
}

case "$scenario" in
  file-basic) run_file_basic ;;
  tcp-loopback) run_tcp_loopback ;;
  *) echo "unknown scenario: $scenario" >&2; exit 2 ;;
esac
sleep 0.5   # let bpftrace drain the last exits before the EXIT trap stops it
