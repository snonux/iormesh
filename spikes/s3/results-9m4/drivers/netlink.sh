#!/usr/bin/env bash
# 9m4: ak4's netlink load re-measured after am4 (reflection-free decode).
# Run as root on iom1 from /opt/9m4. netlink-agg/ is the same run without
# S3_TRACER_FLAGS (aggregation on; 4 rounds, R=4 leaked from burst3).
set -u
out=/opt/9m4/res/netlink
mkdir -p "$out"
echo "start $(date -u +%FT%TZ)" >>"$out/log.txt"
S3_TRACER_CPU=1 S3_LOAD_CPUS=0 S3_TIME=/opt/9m4/bin/cputime S3_REV=bd2bbe4 \
    S3_MODES=msg S3_MSG_LOADS="netlink:100000" S3_RUNS=${R:-5} \
    S3_MSG_BIN=/opt/9m4/bin/msgload S3_TRACER_FLAGS=-aggregate=false S3_TRACER=/opt/9m4/bin/iormesh-full \
    /opt/9m4/spikes/s3/measure.sh "$out/full" >/dev/null 2>>"$out/err.txt"
echo "full rc=$? $(date -u +%T)" >>"$out/log.txt"
echo "end $(date -u +%FT%TZ)" >>"$out/log.txt"
