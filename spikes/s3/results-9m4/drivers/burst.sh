#!/usr/bin/env bash
# 9m4 driver: decision #25 item 2's bystander pairs (syscall-burst, 4
# threads, 500k ops, ak4's settings) per dispatcher variant, interleaved by
# round. Run as root on iom1 from /opt/9m4.
set -u
R=${R:-2}
P=${P:-5}
out=/opt/9m4/res/${NAME:-burst}
mkdir -p "$out"
export S3_TRACER_CPU=1 S3_LOAD_CPUS=0 S3_TIME=/opt/9m4/bin/cputime \
    S3_REV=bd2bbe4+9m4 S3_MODES=bystander S3_THREADS=4 S3_PAIRS=$P \
    S3_OPS=500000 S3_TRACER_FLAGS=-aggregate=false
echo "start $(date -u +%FT%TZ)" >>"$out/log.txt"
for ((r = 1; r <= R; r++)); do
    for v in ${VARIANTS:-empty mirror full}; do
        S3_TRACER=/opt/9m4/bin/iormesh-$v \
            /opt/9m4/spikes/s3/measure.sh "$out/$v-$r" >/dev/null 2>>"$out/err.txt"
        echo "$v $r rc=$? $(date -u +%T)" >>"$out/log.txt"
    done
done
echo "end $(date -u +%FT%TZ)" >>"$out/log.txt"
