#!/usr/bin/env python3
"""Throwaway (S1, task xa4): CPU ns per op from two cost.sh runs.

Usage: costper.py <small.txt> <ops_small> <large.txt> <ops_large>
Per mode: median JVM CPU (user + sys, ms) of each run, then
(large - small) / (ops_large - ops_small) — startup, class loading and
the agent's setup cancel out — plus the median wall ns/op of the large
run and the cost over untraced.
"""
import statistics
import sys


def load(path):
    """mode -> ([wall ns/op], [cpu ms]) from cost.sh output lines."""
    out = {}
    for line in open(path):
        f = line.split()
        if len(f) < 4 or not f[2].isdigit():
            continue
        w, c = out.setdefault(f[0], ([], []))
        w.append(int(f[2]))
        c.append(int(f[3]))
    return out


def main():
    small, n1, large, n2 = sys.argv[1], int(sys.argv[2]), sys.argv[3], int(sys.argv[4])
    a, b = load(small), load(large)
    rows = {}
    for mode in b:
        cpu = (statistics.median(b[mode][1]) - statistics.median(a[mode][1])) \
            * 1e6 / (n2 - n1)
        rows[mode] = (cpu, statistics.median(b[mode][0]), min(b[mode][0]))
    base = rows["untraced"][0]
    print(f"{'mode':9} {'cpu ns/op':>9} {'+cpu':>7} {'wall p50':>9} {'wall min':>9}")
    for mode, (cpu, wall, wmin) in rows.items():
        print(f"{mode:9} {cpu:9.0f} {cpu - base:7.0f} {wall:9d} {wmin:9d}")


if __name__ == "__main__":
    main()
