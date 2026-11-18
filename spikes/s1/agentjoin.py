#!/usr/bin/env python3
"""Throwaway (S1, task wa4): join the agent's Java-level records to the
uprobe-marked syscalls of one JVM, by kernel TID and time containment.

Usage: agentjoin.py <dir> <name>
  reads <dir>/<name>.log (join.bt), <dir>/<name>.hooks and
  <dir>/<name>.recs (agent/), prints every declared-kind syscall of the
  scenario thread inside the marker bracket with: the JNI mark (uprobe),
  the innermost agent hook whose [enter, exit] window contains the
  syscall on the same TID, and the first workload frame of its stack —
  the "declared method" S1's exit criterion asks for.

Clocks: bpftrace's nsecs is CLOCK_BOOTTIME, System.nanoTime() is
CLOCK_MONOTONIC (they differed by ~85 h on the test laptop: suspends).
join.bt prints a (boot, mono) pair at BEGIN and END; agent times are
shifted by the BEGIN offset, and the BEGIN/END offset difference is
printed as the drift (non-zero only if the host suspended mid-run).
"""
import sys

import analyze

WORKLOAD_PKG = "iormesh.workload."


def clock_offsets(log):
    """boot - mono at bpftrace BEGIN and END (END missing if killed)."""
    offs = []
    for line in open(log):
        if line.startswith("C\t"):
            _, boot, mono = line.rstrip("\n").split("\t")
            offs.append(int(boot) - int(mono))
    return offs


def load_recs(prefix, offset):
    """Agent records: (hook name, ktid, enter, exit, stack frames)."""
    names = {}
    for line in open(prefix + ".hooks"):
        i, name = line.rstrip("\n").split("\t")
        names[int(i)] = name
    recs = []
    for line in open(prefix + ".recs"):
        f = line.rstrip("\n").split("\t")
        recs.append({"hook": names.get(int(f[0]), f[0]), "ktid": int(f[1]),
                     "enter": int(f[3]) + offset, "exit": int(f[4]) + offset,
                     "stack": f[5].split(" < ")})
    return recs


def innermost(recs, tid, ns):
    """The hook bracket on tid containing ns that was entered last."""
    best = None
    for r in recs:
        if r["ktid"] == tid and r["enter"] <= ns <= r["exit"]:
            if best is None or r["enter"] > best["enter"]:
                best = r
    return best


def declared_frame(rec):
    """The first workload frame, or the outermost recorded one."""
    if rec is None:
        return "-"
    for frame in rec["stack"]:
        if frame.startswith(WORKLOAD_PKG):
            return frame[len(WORKLOAD_PKG):]
    return "(jdk) " + rec["stack"][-1]


def main():
    d, name = sys.argv[1], sys.argv[2]
    names = analyze.syscall_names()
    tid, mine, _ = analyze.bracket(analyze.parse(f"{d}/{name}.log"))
    offs = clock_offsets(f"{d}/{name}.log")
    print(f"boot-mono offset {offs[0]} ns, drift BEGIN->END "
          f"{offs[-1] - offs[0]} ns")
    recs = load_recs(f"{d}/{name}", offs[0])
    joined = total = 0
    for ev in mine:
        sc = names.get(ev["nr"], str(ev["nr"]))
        if sc not in analyze.DECLARED:
            continue
        rec = innermost(recs, tid, ev["ns"])
        total += 1
        joined += rec is not None
        hook = rec["hook"] if rec else "-"
        print(f"{sc:8} ret={ev['ret']!s:5} {ev['mark'][5:]:42.42} "
              f"{hook:36.36} {declared_frame(rec)}")
    print(f"{name}: {joined}/{total} declared-kind syscalls joined to an "
          f"agent bracket")


if __name__ == "__main__":
    main()
