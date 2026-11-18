#!/usr/bin/env python3
"""Throwaway (S1, task xa4): the tracer side of the agent spool (#27).

Usage:
  spoolread.py ring <spool> [--follow PID] [--recs OUT]
  spoolread.py file <spool> [--recs OUT]

ring: maps the agent's mmap spool (agent/.../Ring.java) MAP_SHARED and
consumes records the way the Go correlator would: wait for a record's
non-zero header word, copy it out, zero it, advance tail. With
--follow it keeps draining while PID lives (concurrent reader, so the
ring can lap); without it drains what is there once.
file: parses the append-only spool (same record framing, no header).

Prints record counts per type and the header's drop counters; --recs
writes the brackets in the agent's mem-mode text format
(hook, ktid, thread, enter, exit, stack) so agentjoin.py reads them.
"""
import mmap
import os
import struct
import sys
import time

PAD, BRACKET, STACK, THREAD = 0, 1, 2, 3
DATA, HEAD, TAIL, DROPPED, DROPPED_BYTES = 4096, 64, 128, 192, 200
MAGIC = 0x314C50534D524F49


class Decoder:
    """Accumulates threads, stacks and brackets from record bodies."""

    def __init__(self):
        self.counts = {PAD: 0, BRACKET: 0, STACK: 0, THREAD: 0}
        self.threads, self.stacks, self.brackets = {}, {}, []

    def record(self, typ, body):
        self.counts[typ] = self.counts.get(typ, 0) + 1
        if typ == BRACKET:
            self.brackets.append(struct.unpack_from("<iiqqqq", body))
        elif typ == STACK:
            h, n = struct.unpack_from("<qi", body)
            self.stacks[h] = body[16:16 + n].decode()
        elif typ == THREAD:
            jtid, ktid, n = struct.unpack_from("<qii", body)
            self.threads[jtid] = body[16:16 + n].decode()

    def write_recs(self, path):
        with open(path, "w") as out:
            for hook, ktid, enter, exit_, stack, jtid in self.brackets:
                out.write(f"{hook}\t{ktid}\t{self.threads.get(jtid, '?')}\t"
                          f"{enter}\t{exit_}\t{self.stacks.get(stack, '')}\n")


def drain_ring(m, cap, dec):
    """Consumes every published record; returns the number consumed."""
    tail = struct.unpack_from("<Q", m, TAIL)[0]
    n = 0
    while True:
        off = DATA + (tail & (cap - 1))
        word = struct.unpack_from("<Q", m, off)[0]
        if word == 0:
            break
        length, typ = word & 0xFFFFFFFF, word >> 32
        dec.record(typ, bytes(m[off + 8:off + length]))
        m[off:off + length] = bytes(length)
        tail += length
        n += 1
        # Production: a release store (Go atomic); x86 is TSO, so a plain
        # store after the zeroing is enough for this spike.
        struct.pack_into("<Q", m, TAIL, tail)
    return n


def read_ring(path, follow_pid):
    f = open(path, "r+b")
    m = mmap.mmap(f.fileno(), 0)
    magic, cap = struct.unpack_from("<QQ", m, 0)
    if magic != MAGIC:
        sys.exit("bad magic")
    dec = Decoder()
    while follow_pid and os.path.exists(f"/proc/{follow_pid}"):
        if drain_ring(m, cap, dec) == 0:
            time.sleep(0.001)
    drain_ring(m, cap, dec)
    dropped, dropped_bytes = struct.unpack_from("<QQ", m, DROPPED)
    return dec, dropped, dropped_bytes


def read_file(path):
    data = open(path, "rb").read()
    dec, off = Decoder(), 0
    while off + 8 <= len(data):
        word = struct.unpack_from("<Q", data, off)[0]
        length, typ = word & 0xFFFFFFFF, word >> 32
        if length == 0:
            break
        dec.record(typ, data[off + 8:off + length])
        off += length
    return dec, 0, 0


def main():
    kind, path, args = sys.argv[1], sys.argv[2], sys.argv[3:]
    follow = args[args.index("--follow") + 1] if "--follow" in args else None
    if kind == "ring":
        dec, dropped, dropped_bytes = read_ring(path, follow)
    else:
        dec, dropped, dropped_bytes = read_file(path)
    names = {PAD: "pad", BRACKET: "bracket", STACK: "stack", THREAD: "thread"}
    print(" ".join(f"{names.get(k, k)}={v}" for k, v in dec.counts.items()),
          f"dropped={dropped} dropped_bytes={dropped_bytes}")
    if "--recs" in args:
        dec.write_recs(args[args.index("--recs") + 1])


if __name__ == "__main__":
    main()
