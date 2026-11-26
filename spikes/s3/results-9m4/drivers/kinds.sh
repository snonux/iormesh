#!/usr/bin/env bash
# 9m4 driver: getppid and close(-1) bystander pairs per dispatcher variant,
# interleaved by round. Run as root on iom1 from /opt/9m4.
set -u
R=${R:-3}
P=${P:-3}
out=/opt/9m4/res/kinds  # kinds-mitoff/ ran with this renamed, R=1 VARIANTS="empty full"
mkdir -p "$out"
export S3_TRACER_CPU=1 S3_LOAD_CPUS=0 S3_TIME=/opt/9m4/bin/cputime \
    S3_KIND_BIN=/opt/9m4/bin/getppid S3_REV=bd2bbe4+9m4 S3_MODES=kind \
    S3_THREADS=1 S3_PAIRS=$P S3_KIND_OPS=${OPS:-2000000}
echo "start $(date -u +%FT%TZ)" >>"$out/log.txt"
for ((r = 1; r <= R; r++)); do
    for v in ${VARIANTS:-empty mirror nocompat full}; do
        for c in getppid close; do
            S3_TRACER=/opt/9m4/bin/iormesh-$v S3_KIND_CALL=$c \
                /opt/9m4/spikes/s3/measure.sh "$out/$v-$c-$r" >/dev/null 2>>"$out/err.txt"
            echo "$v $c $r rc=$? $(date -u +%T)" >>"$out/log.txt"
        done
    done
done
echo "end $(date -u +%FT%TZ)" >>"$out/log.txt"
