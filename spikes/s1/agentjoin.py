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

Clocks: System.nanoTime() is CLOCK_MONOTONIC. Since task xa4 join.bt
stamps with nsecs(monotonic) too and says so in a "CLOCK monotonic"
line: then agent times are used as they are. Logs from wa4 (no CLOCK
line) stamped CLOCK_BOOTTIME; agent times are then shifted by the BEGIN
(boot, mono) pair — biased by up to ~160 ns (spikes/s1/clock), which
the reported smallest window gap must exceed.
"""
import sys

import analyze

WORKLOAD_PKG = "iormesh.workload."

# Agent hook -> the JNI natives (uprobe marks, "Java_" stripped) that hook
# calls directly (HookTable.java). A joined syscall whose mark is not in
# its hook's set is a mismatch: the two joins disagree.
HOOK_NATIVES = {
    "java/io/FileOutputStream.open": {"java_io_FileOutputStream_open0"},
    "java/io/FileOutputStream.write": {"java_io_FileOutputStream_writeBytes",
                                       "java_io_FileOutputStream_write"},
    "java/io/FileInputStream.open": {"java_io_FileInputStream_open0"},
    "java/io/FileInputStream.read": {"java_io_FileInputStream_readBytes",
                                     "java_io_FileInputStream_read0"},
    "java/io/FileDescriptor.close": {"java_io_FileDescriptor_close0"},
    "java/io/RandomAccessFile.readBytes": {"java_io_RandomAccessFile_readBytes0"},
    "java/io/RandomAccessFile.seek": {"java_io_RandomAccessFile_seek0"},
    "sun/nio/fs/UnixNativeDispatcher.unlink":
        {"sun_nio_fs_UnixNativeDispatcher_unlink0"},
    "sun/nio/ch/Net.socket": {"sun_nio_ch_Net_socket0"},
    "sun/nio/ch/Net.serverSocket": {"sun_nio_ch_Net_socket0"},
    "sun/nio/ch/Net.bind": {"sun_nio_ch_Net_bind0"},
    "sun/nio/ch/Net.connect": {"sun_nio_ch_Net_connect0"},
    "sun/nio/ch/NioSocketImpl.listen": {"sun_nio_ch_Net_listen"},
    "sun/nio/ch/NioSocketImpl.accept": {"sun_nio_ch_Net_accept"},
    "sun/nio/ch/SocketDispatcher.read": {"sun_nio_ch_SocketDispatcher_read0",
                                         "sun_nio_ch_SocketDispatcher_readv0"},
    "sun/nio/ch/SocketDispatcher.write": {"sun_nio_ch_SocketDispatcher_write0",
                                          "sun_nio_ch_SocketDispatcher_writev0"},
    "sun/nio/ch/UnixDispatcher.close": {"sun_nio_ch_UnixDispatcher_close0"},
}


def clock_offsets(log):
    """boot - mono at bpftrace BEGIN and END (END missing if killed), or
    [0] when the log's stamps are monotonic already (CLOCK line)."""
    offs = []
    for line in open(log):
        if line.startswith("CLOCK\tmonotonic"):
            return [0]
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
    stats = {"total": 0, "joined": 0, "scenario": 0, "mismatch": 0,
             "gap": None}
    for ev in mine:
        sc = names.get(ev["nr"], str(ev["nr"]))
        if sc in analyze.DECLARED:
            report(ev, sc, innermost(recs, tid, ev["ns"]), stats)
    print(f"{name}: {stats['joined']}/{stats['total']} declared-kind "
          f"syscalls joined to an agent bracket, {stats['scenario']} with a "
          f"workload frame, {stats['mismatch']} hook/mark mismatches, "
          f"smallest hook-window-to-syscall gap {stats['gap']} ns")


def report(ev, sc, rec, stats):
    """Prints one syscall's join and folds it into stats."""
    stats["total"] += 1
    mark = ev["mark"][5:]
    if rec is not None:
        stats["joined"] += 1
        frame = declared_frame(rec)
        stats["scenario"] += not frame.startswith("(jdk)")
        if mark not in HOOK_NATIVES.get(rec["hook"], set()):
            stats["mismatch"] += 1
            print(f"MISMATCH hook {rec['hook']} vs mark {ev['mark']}")
        # How far the syscall sits inside the agent window: the margin a
        # boot/mono offset error would have to exceed to break the join.
        gap = min(ev["ns"] - rec["enter"], rec["exit"] - ev["ns"])
        stats["gap"] = gap if stats["gap"] is None else min(stats["gap"], gap)
    hook = rec["hook"] if rec else "-"
    print(f"{sc:8} ret={ev['ret']!s:5} {mark:42.42} {hook:36.36} "
          f"{declared_frame(rec)}")


if __name__ == "__main__":
    main()
