#!/usr/bin/env python3
"""Throwaway (S1, task wa4): read a join.bt log, cut it to the scenario
bracket (begin/end marker openat on the scenario thread) and print each
syscall with the JNI function that was active on its TID.

Usage: analyze.py <log> [--all] [--pairs]
  --all    also summarize the other threads' syscalls inside the bracket,
           to list what stays unbracketed.
  --pairs  count (mark, syscall) pairs on the scenario thread: a pair
           whose syscall the native never issues itself (pread of the
           jimage under open0, say) is a mark leaking onto JVM work.

The declared events (expect.json) are the I/O syscalls of the scenario
thread; the summary line counts how many of them carried a mark.
"""
import collections
import subprocess
import sys

# x86_64 nr 335 is the uretprobe trampoline syscall (Linux >= 6.11): every
# uretprobe return enters it and never exits. It is noise this spike's own
# probes cause, so it is dropped (and counted) rather than listed.
URETPROBE_NR = 335

# The syscalls expect.json declares for file-basic and tcp-loopback.
DECLARED = {"openat", "read", "write", "close", "connect", "accept",
            "accept4", "unlink"}


def syscall_names():
    """nr -> name for this arch, from ausyscall (audit package)."""
    out = subprocess.run(["ausyscall", "--dump"], capture_output=True,
                         text=True, check=True).stdout
    names = {}
    for line in out.splitlines()[1:]:
        nr, name = line.split("\t")
        names[int(nr)] = name
    return names


def parse(path):
    """Returns enter events in order, each with its exit ret and path."""
    events, open_by_tid, paths = [], {}, {}
    for line in open(path, errors="replace"):
        f = line.rstrip("\n").split("\t")
        if f[0] == "E" and len(f) == 9 and int(f[4]) == URETPROBE_NR:
            continue
        if f[0] == "E" and len(f) == 9:
            ev = {"ns": int(f[1]), "tid": int(f[2]), "comm": f[3],
                  "nr": int(f[4]), "a": f[5:8], "mark": f[8],
                  "ret": None, "path": None}
            events.append(ev)
            open_by_tid[ev["tid"]] = ev
        elif f[0] == "X" and len(f) == 5:
            ev = open_by_tid.pop(int(f[2]), None)
            if ev is not None and ev["nr"] == int(f[3]):
                ev["ret"] = int(f[4])
        elif f[0] == "P" and len(f) == 5:
            paths[int(f[2])] = f[4]
            ev = open_by_tid.get(int(f[2]))
            if ev is not None:
                ev["path"] = f[4]
    return events


def bracket(events):
    """The scenario thread's events between its begin and end markers."""
    begin = end = None
    for i, ev in enumerate(events):
        p = ev["path"] or ""
        if p.startswith("/iormesh-marker/") and p.endswith("/begin"):
            begin = i
        elif p.startswith("/iormesh-marker/") and p.endswith("/end"):
            end = i
    if begin is None or end is None:
        sys.exit("no complete marker bracket in log")
    tid = events[begin]["tid"]
    inner = events[begin + 1:end]
    return tid, [e for e in inner if e["tid"] == tid], \
        [e for e in inner if e["tid"] != tid]


def main():
    names = syscall_names()
    events = parse(sys.argv[1])
    tid, mine, others = bracket(events)
    hit = miss = 0
    print(f"scenario thread tid={tid}")
    for ev in mine:
        name = names.get(ev["nr"], str(ev["nr"]))
        declared = name in DECLARED
        if declared:
            hit, miss = (hit + 1, miss) if ev["mark"] != "-" else (hit, miss + 1)
        flag = "*" if declared else " "
        extra = ev["path"] or ",".join(ev["a"][:1])
        print(f" {flag} {name:12} {extra:50.50} ret={ev['ret']!s:6} "
              f"{ev['mark']}")
    print(f"declared-kind syscalls on scenario thread: {hit} marked, "
          f"{miss} unmarked")
    if "--all" in sys.argv:
        summarize(names, others, "other threads inside bracket")
    if "--pairs" in sys.argv:
        summarize(names, mine, "scenario thread (comm, syscall, mark)")


def summarize(names, events, title):
    count = collections.Counter(
        (e["comm"], names.get(e["nr"], str(e["nr"])), e["mark"])
        for e in events)
    print(f"-- {title}")
    for (comm, name, mark), n in sorted(count.items()):
        print(f"   {n:5} {comm:16} {name:16} {mark}")


if __name__ == "__main__":
    main()
