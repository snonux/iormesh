#!/usr/bin/env python3
"""S2 spike (throwaway, task 6b4): evaluate the join keys of one rig pass.

Usage: join.py PASSDIR   (isn.log, hostA.syn.txt, hostB.syn.txt,
                          conntrack.txt as written by rig.sh)

1. Rebuilds connections per (netns, struct sock) from isn.log's state and
   accept events.
2. Derives the ISN pair at each event three ways (see derive: direct,
   the guide's naive rebuild, and the role-aware rebuild the runs showed
   to hold) and checks them against the SYN / SYN-ACK sequence numbers
   tcpdump saw in that namespace.
3. Joins client (hostA) and server (hostB) connections on the ISN pair, and
   separately on the conntrack original/reply tuples, and reports matches,
   ambiguities and misses.
"""

import collections
import re
import sys

M32 = 1 << 32
STATE = {1: "ESTABLISHED", 2: "SYN_SENT", 3: "SYN_RECV", 4: "FIN_WAIT1",
         5: "FIN_WAIT2", 6: "TIME_WAIT", 7: "CLOSE", 8: "CLOSE_WAIT",
         9: "LAST_ACK", 10: "LISTEN", 11: "CLOSING", -1: "accept"}
PORT = 8080


def kv(line):
    return dict(f.split("=", 1) for f in line.split() if "=" in f)


def load_events(path):
    socks, cts = [], []
    for line in open(path):
        if line.startswith("ev=ct"):
            cts.append(kv(line))
        elif line.startswith("ev="):
            e = kv(line)
            for k in ("ns", "t", "old", "new", "snd_una", "rcv_nxt",
                      "bytes_acked", "bytes_received"):
                e[k] = int(e[k])
            socks.append(e)
    socks.sort(key=lambda e: e["t"])
    return socks, cts


def connections(events):
    """Splits each socket's event stream into connections: one starts when
    a socket enters SYN_SENT (from CLOSE) or SYN_RECV (a child cloned from
    the listener, so old=LISTEN). struct sock addresses are recycled, so
    the address alone is not a connection."""
    open_conn, last, conns = {}, {}, []
    for e in events:
        key = (e["ns"], e["sk"])
        if e["new"] in (2, 3):
            open_conn[key] = last[key] = {"ns": e["ns"], "events": []}
            conns.append(open_conn[key])
        if key in open_conn:
            open_conn[key]["events"].append(e)
        elif e["new"] == -1 and key in last:
            # accept() of a child that was reset in the accept queue: its
            # TCP_CLOSE came first, the accept still belongs to it.
            last[key]["events"].append(e)
        if e["new"] == 7:
            open_conn.pop(key, None)
    for c in conns:
        est = [e for e in c["events"] if e["new"] == 1]
        ref = est[0] if est else c["events"][-1]
        c["src"], c["dst"] = ref["src"], ref["dst"]
        c["t"] = c["events"][0]["t"]
        c["role"] = "server" if c["src"].endswith(f":{PORT}") else "client"
    return conns


def derive(e, role):
    """The (own iss, peer iss) pair three ways. 'naive' is the guide's
    hypothesis (subtract one for the SYN on both counters); 'rebuilt' is
    what the run showed to hold: the active opener's bytes_acked already
    counts its own SYN (tcp_snd_una_update runs for the SYN-ACK), the
    passive child's does not (snd_una starts at iss + 1 when the child is
    cloned), and bytes_received never counts the peer's SYN but does count
    its FIN, like rcv_nxt."""
    syn = 0 if role == "client" else 1
    return {
        "direct": ((e["snd_una"] - 1) % M32, (e["rcv_nxt"] - 1) % M32),
        "naive": ((e["snd_una"] - e["bytes_acked"] - 1) % M32,
                  (e["rcv_nxt"] - e["bytes_received"] - 1) % M32),
        "rebuilt": ((e["snd_una"] - e["bytes_acked"] - syn) % M32,
                    (e["rcv_nxt"] - e["bytes_received"] - 1) % M32),
    }


SYN_RE = re.compile(r"IP (\S+)\.(\d+) > (\S+)\.(\d+): Flags \[(S\.?)\], "
                    r"seq (\d+)")


def truth(path):
    """Returns {(local, remote) tuple of the side that sent the SYN:
    [(iss, irs)]} from the tcpdump SYN lines, in capture order."""
    syn, synack = {}, {}
    for line in open(path):
        m = SYN_RE.search(line)
        if not m:
            continue
        a, ap, b, bp, flags, seq = m.groups()
        if flags == "S":
            syn.setdefault((f"{a}:{ap}", f"{b}:{bp}"), []).append(int(seq))
        else:
            synack.setdefault((f"{b}:{bp}", f"{a}:{ap}"), []).append(int(seq))
    pairs = {}
    for t, isss in syn.items():
        irss = list(dict.fromkeys(synack.get(t, [])))
        isss = list(dict.fromkeys(isss))
        pairs[t] = list(zip(isss, irss))
    return pairs


def assign_truth(conns, truths):
    """Gives each connection the (own iss, peer iss) the pcap shows for it.
    A reused tuple has several SYNs; the k-th connection on a tuple (in
    event time order) gets the k-th distinct SYN of that tuple, so a
    derivation must hit its own connection's pair, not any of them."""
    seen = collections.Counter()
    for c in sorted(conns, key=lambda c: c["t"]):
        if c["role"] == "client":
            key = ("A", c["src"], c["dst"])
            pairs = truths["A"].get((c["src"], c["dst"]), [])
        else:
            key = ("B", c["dst"], c["src"])
            pairs = [(r, i) for i, r in
                     truths["B"].get((c["dst"], c["src"]), [])]
        k = seen[key]
        seen[key] += 1
        c["truth"] = pairs[k] if k < len(pairs) else None


def pcap_seq_equal(truths, out):
    """NAT must leave sequence numbers alone: every (SYN seq, SYN-ACK seq)
    pair hostA saw must also appear in hostB's capture."""
    a = [p for ps in truths["A"].values() for p in ps]
    b = {p for ps in truths["B"].values() for p in ps}
    out.append(f"pcap seq pairs hostA found in hostB: "
               f"{sum(p in b for p in a)}/{len(a)}")


def check_methods(conns, truths, out):
    """Counts, per (role, event, method), how often the derived pair is
    the true one."""
    score = collections.defaultdict(lambda: [0, 0])
    for c in conns:
        for e in c["events"]:
            ev = f"{STATE.get(e['old'])}->{STATE.get(e['new'])}"
            for name, p in derive(e, c["role"]).items():
                s = score[(c["mode"], c["role"], ev, name)]
                s[0] += p == c["truth"]
                s[1] += 1
    out.append("ISN derivation vs tcpdump (ok/total) per mode, role, "
               "event, method:")
    for k in sorted(score):
        ok, n = score[k]
        out.append(f"  {k[0]:9} {k[1]:6} {k[2]:24} {k[3]:7} {ok}/{n}")


def key_at_owner(c):
    """The pair as iormesh could take it: the client at its ESTABLISHED
    transition, the server at accept exit, both rebuilt."""
    want = (-1, -1) if c["role"] == "server" else (2, 1)
    for e in c["events"]:
        if (e["old"], e["new"]) == want:
            return derive(e, c["role"])["rebuilt"]
    return None


def isn_join(conns, out):
    clients = [c for c in conns if c["role"] == "client"]
    servers = collections.defaultdict(list)
    for s in conns:
        if s["role"] == "server" and key_at_owner(s):
            iss, irs = key_at_owner(s)
            servers[(irs, iss)].append(s)
    ok = amb = miss = 0
    for c in clients:
        hits = servers.get(key_at_owner(c), [])
        ok += len(hits) == 1
        amb += len(hits) > 1
        miss += not hits
        c["isn_peer"] = hits[0] if len(hits) == 1 else None
    tup_same = sum(1 for c in clients if c["isn_peer"] and
                   (c["isn_peer"]["dst"], c["isn_peer"]["src"]) ==
                   (c["src"], c["dst"]))
    out.append(f"ISN-pair join: clients={len(clients)} "
               f"servers={sum(map(len, servers.values()))} unique={ok} "
               f"ambiguous={amb} missing={miss}; "
               f"pairs whose 4-tuple is identical on both ends={tup_same}")
    tuples = collections.Counter((c["src"], c["dst"]) for c in clients)
    out.append(f"client 4-tuples: {len(clients)} connections, "
               f"{len(tuples)} distinct (max reuse {max(tuples.values())})")


CT_RE = re.compile(r"src=(\S+) dst=(\S+) sport=(\d+) dport=(\d+)")


def ct_dump(path):
    rows = []
    for line in open(path):
        m = CT_RE.findall(line)
        if len(m) == 2 and "dport=%d" % PORT in line:
            (s, d, sp, dp), (rs, rd, rsp, rdp) = m
            rows.append((f"{s}:{sp}>{d}:{dp}", f"{rs}:{rsp}>{rd}:{rdp}",
                         line.split()[5]))
    return rows


def ct_join(conns, cts, dump, out):
    """Client tuple == conntrack original, server tuple reversed ==
    conntrack reply. Three ways: by original tuple alone (ambiguous where a
    tuple was reused), by tuple plus order (the k-th connection on a tuple
    takes the k-th insert - stands in for a time-window match, which across
    hosts needs clock reconciliation), and against the /proc snapshot read
    at the end of the pass."""
    inserts = collections.defaultdict(list)
    for ct in cts:
        inserts[ct["orig"]].append(ct)
    snap = {orig: reply for orig, reply, _ in dump}
    ok = amb = miss = ordered = snap_ok = snap_wrong = snap_gone = 0
    seen = collections.Counter()
    clients = sorted((c for c in conns if c["role"] == "client"),
                     key=lambda c: c["t"])
    for c in clients:
        orig = f"{c['src']}>{c['dst']}"
        hits = inserts.get(orig, [])
        ok += len(hits) == 1
        amb += len(hits) > 1
        miss += not hits
        k = seen[orig]
        seen[orig] += 1
        p = c.get("isn_peer")
        if not p:
            continue
        server = f"{p['src']}>{p['dst']}"
        ordered += k < len(hits) and hits[k]["reply"] == server
        if orig not in snap:
            snap_gone += 1
        elif snap[orig] == server:
            snap_ok += 1
        else:
            snap_wrong += 1
    out.append(f"conntrack inserts (confirm events in nat): {len(cts)}")
    out.append(f"conntrack-insert join by tuple: unique={ok} ambiguous={amb} "
               f"missing={miss}; by tuple+order agreeing with ISN "
               f"join={ordered}")
    out.append(f"/proc/net/nf_conntrack at end: {len(dump)} entries "
               f"(states {dict(collections.Counter(r[2] for r in dump))}); "
               f"snapshot join vs ISN join: agree={snap_ok} "
               f"wrong-server={snap_wrong} no-entry={snap_gone}")
    seq_ok = sum(1 for c in clients if c.get("isn_peer") and any(
        (int(ct["td_end0"]) - 1) % M32 == key_at_owner(c)[0]
        for ct in inserts.get(f"{c['src']}>{c['dst']}", [])))
    out.append(f"conntrack td_end0-1 == client iss (non-TFO expected): "
               f"{seq_ok}")


def isn_deltas(conns, out):
    """ISN steps between consecutive connections of a reused tuple: on the
    TIME_WAIT path the client's new ISN is the old snd_nxt + 65537, on the
    RST path both sides draw from the clock."""
    for mode in ("reuse-tw", "reuse-rst"):
        cl = sorted((c for c in conns if c["mode"] == mode and
                     c["role"] == "client" and c["isn_peer"]),
                    key=lambda c: c["t"])
        ci = [key_at_owner(c)[0] for c in cl]
        si = [key_at_owner(c["isn_peer"])[0] for c in cl]
        d = lambda xs: [(b - a) % M32 for a, b in zip(xs, xs[1:])]
        out.append(f"{mode} ISN deltas: client {d(ci)} server {d(si)}")


TFO_COUNT, PINGPONG_COUNT = 6, 4  # rig.sh's last two phases, in order


def tag_modes(conns):
    """Labels connections by workload phase: reuse-tw uses source port
    45678, reuse-rst 45679, TFO is the last 6 client connections and
    ping-pong the 4 before them (rig.sh runs them last, in that order);
    servers inherit the label through the ISN join later. conns must be in
    time order."""
    clients = [c for c in conns if c["role"] == "client"]
    tfo = len(clients) - TFO_COUNT
    for c in conns:
        c["mode"] = "?"
    for i, c in enumerate(clients):
        c["mode"] = ("reuse-tw" if c["src"].endswith(":45678") else
                     "reuse-rst" if c["src"].endswith(":45679") else
                     "tfo" if i >= tfo else
                     "pingpong" if i >= tfo - PINGPONG_COUNT else "plain")


def tfo_evidence(d, out):
    """Counts SYNs that carried payload on the client side: proof the TFO
    phase really put data in the SYN."""
    n = sum(1 for line in open(f"{d}/hostA.syn.txt")
            if "Flags [S]," in line and not line.rstrip().endswith(
                "length 0"))
    out.append(f"client SYNs with payload (TFO): {n}")
    for side in ("hostA", "hostB"):
        rst = sum(1 for _ in open(f"{d}/{side}.rst.txt"))
        out.append(f"RST packets in {side} capture: {rst}")


def main(d):
    events, cts = load_events(f"{d}/isn.log")
    conns = connections(events)
    tag_modes(conns)
    truths = {"A": truth(f"{d}/hostA.syn.txt"),
              "B": truth(f"{d}/hostB.syn.txt")}
    out = []
    pcap_seq_equal(truths, out)
    isn_join(conns, out)
    for c in conns:
        if c["role"] == "client" and c["isn_peer"]:
            c["isn_peer"]["mode"] = c["mode"]
    assign_truth(conns, truths)
    check_methods(conns, truths, out)
    isn_deltas(conns, out)
    ct_join(conns, cts, ct_dump(f"{d}/conntrack.txt"), out)
    tfo_evidence(d, out)
    print("\n".join(out))


if __name__ == "__main__":
    main(sys.argv[1])
