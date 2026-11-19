#!/usr/bin/env bash
# insn.sh BIN BENCH N: user instructions and cycles per op of one Go
# benchmark, as the difference of a 2N and an N run over N (setup cancels).
set -euo pipefail
bin=$1 bench=$2 n=$3
run() {
    perf stat -x, -e instructions:u,cycles:u taskset -c 3 "$bin" -test.run '^$' \
        -test.bench "^Benchmark${bench}\$" -test.benchtime "${1}x" -test.cpu 1 2>&1 >/dev/null |
        awk -F, '/instructions/{i=$1} /cycles/{c=$1} END{print i, c}'
}
read -r i1 c1 < <(run "$n")
read -r i2 c2 < <(run $((2 * n)))
awk -v i1="$i1" -v i2="$i2" -v c1="$c1" -v c2="$c2" -v n="$n" -v b="$bench" \
    'BEGIN{printf "%s insn/op=%.0f cycles/op=%.0f\n", b, (i2-i1)/n, (c2-c1)/n}'
