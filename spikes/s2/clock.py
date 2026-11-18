#!/usr/bin/env python3
"""S2 spike (throwaway, task 7b4): clock-offset bounds from the traffic.

Usage: clock.py PASSDIR   (clock.hostA.log, clock.hostB.log and
                           clock.injected as written by rig.sh)

Experiment 3 of docs/implementation.md, S2. With theta = clock_B - clock_A,
every message on a fused connection is one inequality (a receive always
completes after its send started):

    A->B bytes:  theta < recv_B - send_A        (upper bound)
    B->A bytes:  theta > send_B - recv_A        (lower bound)

send is the send syscall's enter, recv the recv syscall's exit. The two
ends of a message are matched by TCP sequence number, which NAT leaves
alone: the recv whose last byte is b pairs with the send that queued b
(the latest-starting send any byte of that recv came from). Connections
are matched across the hosts by the ISN pair (experiment 1's key).

The interval is the intersection of all inequalities; the injected skew
(rig.sh's per-tracer test-only offsets) must lie inside it.
"""

import bisect
import collections
import statistics
import sys

sys.dont_write_bytecode = True  # no __pycache__ next to the spike
import join  # noqa: E402  same directory: derive, tag_modes, kv, M32, PORT

US = 1000.0


def load(path):
    """Events of one host's clock.bt, in the order they happened there."""
    out = []
    for line in open(path):
        if not line.startswith("ev="):
            continue
        e = join.kv(line)
        for k, v in e.items():
            if k not in ("ev", "sk", "src", "dst"):
                e[k] = int(v)
        out.append(e)
    out.sort(key=lambda e: e["at"])
    return out


def connections(events):
    """Splits each struct sock's events into connections. A connection
    runs from its SYN_SENT / SYN_RECV transition to the next one on the
    same sock (addresses are recycled); its first CLOSE carries the key
    fields, but data can still be read after it (see clock.bt)."""
    cur, conns = {}, []
    for e in events:
        sk = e["sk"]
        if e["ev"] == "state" and e["new"] in (2, 3):
            cur[sk] = {"t": e["at"], "close": None, "send": [], "recv": []}
            conns.append(cur[sk])
        elif sk not in cur:
            continue  # sock opened before the tracer attached
        elif e["ev"] == "state":
            cur[sk]["close"] = cur[sk]["close"] or e
        else:
            cur[sk][e["ev"]].append(e)
    return [c for c in conns if c["close"]]


def keyed(conns):
    """Adds role, 4-tuple, ISN pair (as (client iss, server iss)) and the
    stream offsets of every data event: offset = seq - (iss + 1), the same
    number on both hosts for the same byte."""
    for c in conns:
        e = c["close"]
        c["src"], c["dst"] = e["src"], e["dst"]
        c["role"] = ("server" if e["src"].endswith(f":{join.PORT}")
                     else "client")
        own, peer = join.derive(e, c["role"])["rebuilt"]
        c["key"] = (own, peer) if c["role"] == "client" else (peer, own)
        c["sends"] = sorted(((e["end"] - own - 1) % join.M32, e["n"], e["t"])
                            for e in c["send"])
        c["recvs"] = [((e["end"] - peer - 1) % join.M32, e["t"])
                      for e in c["recv"]]
    return conns


def one_way(sends, recvs):
    """[(recv t, send t)] for every recv: the send that queued the recv's
    last byte. A recv without such a send (bytes the other tracer did not
    see) is returned as None and counted by the caller."""
    ends = [end for end, _, _ in sends]
    pairs = []
    for end, t_recv in recvs:
        i = bisect.bisect_left(ends, end)
        hit = i < len(sends) and sends[i][0] - sends[i][1] < end
        pairs.append((t_recv, sends[i][2]) if hit else None)
    return pairs


def constraints(client, server):
    """(uppers, lowers, unmatched) for one fused connection, in ns."""
    ab = one_way(client["sends"], server["recvs"])
    ba = one_way(server["sends"], client["recvs"])
    uppers = [r - s for p in ab if p for r, s in [p]]
    lowers = [s - r for p in ba if p for r, s in [p]]
    unmatched = sum(p is None for p in ab + ba)
    return uppers, lowers, unmatched


def fuse(a, b):
    """Pairs hostA's client connections with hostB's server connections by
    ISN pair; returns [(client, server)] in client time order."""
    servers = collections.defaultdict(list)
    for s in b:
        if s["role"] == "server":
            servers[s["key"]].append(s)
    fused = []
    for c in a:
        hits = servers.get(c["key"], [])
        if len(hits) == 1:
            fused.append((c, hits[0]))
    return fused


def interval(pairs):
    """The intersection over all constraints of the given fused pairs."""
    up, lo = [], []
    for c, s in pairs:
        u, l, _ = constraints(c, s)
        up += u
        lo += l
    return (max(lo) if lo else None, min(up) if up else None, len(lo),
            len(up))


def fmt(lo, hi, theta):
    if lo is None or hi is None:
        return f"lo={lo} hi={hi} (one direction has no constraint)"
    ok = lo < theta < hi
    return (f"[{(lo - theta) / US:+.1f}, {(hi - theta) / US:+.1f}] us "
            f"around theta, width {(hi - lo) / US:.1f} us, contains "
            f"theta: {ok}, midpoint error "
            f"{((lo + hi) / 2 - theta) / US:+.1f} us")


def report(fused, theta, out):
    lo, hi, nl, nu = interval(fused)
    unmatched = sum(constraints(c, s)[2] for c, s in fused)
    out.append(f"injected theta = clock_B - clock_A = {theta} ns")
    out.append(f"fused connections: {len(fused)}; constraints: {nu} upper "
               f"(A->B), {nl} lower (B->A); recvs without a send: "
               f"{unmatched}")
    out.append(f"bound, all connections: lo={lo} hi={hi}")
    out.append(f"  {fmt(lo, hi, theta)}")
    by_mode = collections.defaultdict(list)
    for c, s in fused:
        by_mode[c["mode"]].append((c, s))
    for mode, pairs in sorted(by_mode.items()):
        out.append(f"  only {mode:9} ({len(pairs):2} conns): "
                   f"{fmt(*interval(pairs)[:2], theta)}")
    rest = [p for p in fused if p[0]["mode"] != "pingpong"]
    out.append(f"  without pingpong ({len(rest)} conns): "
               f"{fmt(*interval(rest)[:2], theta)}")
    slack(fused, theta, out)
    convergence(fused, theta, out)


def slack(fused, theta, out):
    """How far each single constraint sits from the true skew: the one-way
    gap between a send's enter and its recv's exit, as the two clocks
    would show it without skew."""
    up, lo = [], []
    for c, s in fused:
        u, l, _ = constraints(c, s)
        up += [x - theta for x in u]
        lo += [theta - x for x in l]
    for name, xs in (("A->B", up), ("B->A", lo)):
        xs.sort()
        out.append(f"one-way gap {name} (us): min {xs[0] / US:.1f} "
                   f"p50 {statistics.median(xs) / US:.1f} "
                   f"max {xs[-1] / US:.1f} (n={len(xs)}); negative = "
                   f"violated: {sum(x <= 0 for x in xs)}")


def convergence(fused, theta, out):
    """Width after the first k fused connections, in time order."""
    steps = []
    for k in (1, 2, 5, 10, 20, len(fused)):
        if k <= len(fused):
            lo, hi, _, _ = interval(fused[:k])
            w = f"{(hi - lo) / US:.1f}" if lo is not None and \
                hi is not None else "open"
            steps.append(f"{k}:{w}")
    out.append(f"width (us) after the first k connections: "
               f"{' '.join(steps)}")


def main(d):
    theta = int(open(f"{d}/clock.injected").read().split("=")[1])
    a = keyed(connections(load(f"{d}/clock.hostA.log")))
    b = keyed(connections(load(f"{d}/clock.hostB.log")))
    clients = sorted((c for c in a if c["role"] == "client"),
                     key=lambda c: c["t"])
    join.tag_modes(clients)
    fused = fuse(clients, b)
    out = [f"hostA connections {len(a)}, hostB {len(b)}, fused by ISN pair "
           f"{len(fused)}"]
    report(fused, theta, out)
    print("\n".join(out))


if __name__ == "__main__":
    main(sys.argv[1])
