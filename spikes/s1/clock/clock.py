#!/usr/bin/env python3
"""Throwaway (S1, task xa4): checks ClockProbe's nanoTime brackets
against clock.bt's kernel stamps of the same writes (k-th bracket ↔ k-th
3-byte write; one thread, so the order is the same on both sides).

Reports, per clock domain, how many kernel stamps fall outside their
bracket and the smallest margin (distance to the nearer bracket edge):
  mono          — bpf_ktime_get_ns vs nanoTime, no conversion at all;
  boot→mono     — bpf boot stamp minus the boot-mono offset sampled in
                  clock.bt's BEGIN, i.e. what agentjoin.py does today;
and the spread of (boot - mono) read back to back in each probe.
"""
import statistics
import sys


def load(out):
    brackets = [tuple(map(int, l.split())) for l in open(f"{out}/brackets.txt")]
    stamps, pairs = [], []
    for line in open(f"{out}/clock.log"):
        f = line.rstrip("\n").split("\t")
        if f[0] == "W":
            stamps.append((int(f[1]), int(f[2])))
        elif f[0] == "C":
            pairs.append(int(f[1]) - int(f[2]))
    return brackets, stamps, pairs


def check(brackets, ks):
    """(outside count, min margin ns, median margin ns) of ks in brackets."""
    outside, margins = 0, []
    for (lo, hi), k in zip(brackets, ks):
        if not lo <= k <= hi:
            outside += 1
        margins.append(min(k - lo, hi - k))
    return outside, min(margins), statistics.median(margins)


def main():
    out = sys.argv[1]
    brackets, stamps, pairs = load(out)
    if len(brackets) != len(stamps):
        sys.exit(f"count mismatch: {len(brackets)} brackets, {len(stamps)} stamps")
    begin_off = pairs[0]
    mono = [m for m, _ in stamps]
    conv = [b - begin_off for _, b in stamps]
    probe_off = [b - m for m, b in stamps]
    width = [hi - lo for lo, hi in brackets]
    print(f"writes={len(stamps)} bracket_ns p50={statistics.median(width):.0f} "
          f"min={min(width)}")
    for name, ks in (("mono", mono), ("boot->mono", conv)):
        o, mn, med = check(brackets, ks)
        print(f"{name:11s} outside={o} min_margin_ns={mn} p50_margin_ns={med:.0f}")
    d = [o - begin_off for o in probe_off]
    print(f"in-probe (boot-mono) - BEGIN offset: min={min(d)} max={max(d)} "
          f"p50={statistics.median(d):.0f} ns; BEGIN->END drift "
          f"{pairs[-1] - pairs[0] if len(pairs) > 1 else 'n/a'} ns")


if __name__ == "__main__":
    main()
