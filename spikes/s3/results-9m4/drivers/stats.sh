#!/usr/bin/env bash
# 9m4: the dispatchers' own run time per run (kernel.bpf_stats_enabled) for a
# bystander load beside a tracer of an idle sleep. Run as root on iom1.
# Usage: stats.sh VARIANT LOAD   (LOAD: burst | close | getppid)
set -u
v=$1 load=$2
w=$(mktemp -d)
sleep 600 &
idle=$!
taskset -c 1 /opt/9m4/bin/iormesh-$v run -pid $idle -project s3 -out $w/trace \
    -duration 10m -ready $w/ready -aggregate=false 2>$w/err &
tp=$!
for ((i = 0; i < 600; i++)); do [ -e $w/ready ] && break; sleep 0.1; done
snap() {
    bpftool prog show -j | python3 -c '
import json,sys
for p in json.load(sys.stdin):
    if p.get("name","").startswith("handle_raw_sys"):
        print(p["name"], p.get("run_cnt",0), p.get("run_time_ns",0))'
}
sysctl -q kernel.bpf_stats_enabled=1
a=$(snap)
case $load in
burst)
    mkdir $w/b
    taskset -c 0 java -jar /opt/9m4/bin/workload.jar syscall-burst --ops 500000 \
        --threads 4 --dir $w/b >/dev/null 2>&1
    ;;
*) taskset -c 0 /opt/9m4/bin/getppid 1 2000000 $load >/dev/null ;;
esac
b=$(snap)
sysctl -q kernel.bpf_stats_enabled=0
kill $idle
wait $tp
paste <(echo "$a") <(echo "$b") | awk -v v=$v -v l=$load \
    '{n=$5-$2; t=$6-$3; printf "%s %s %s runs=%d ns_per_run=%.1f\n", v, l, $1, n, n ? t/n : 0}'
rm -rf $w
