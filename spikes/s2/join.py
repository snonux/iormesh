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
    open_conn, conns = {}, []
    for e in events:
        key = (e["ns"], e["sk"])
        if e["new"] in (2, 3):
            open_conn[key] = {"ns": e["ns"], "events": []}
            conns.append(open_conn[key])
        if key in open_conn:
            open_conn[key]["events"].append(e)
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


def true_pair(c, truths):
    """The (own iss, peer iss) the pcap shows for this connection's
    tuple, or the candidates if the tuple was reused."""
    if c["role"] == "client":
        return truths["A"].get((c["src"], c["dst"]), [])
    return [(r, i) for i, r in truths["B"].get((c["dst"], c["src"]), [])]


def check_methods(conns, truths, out):
    """Counts, per (role, event, method), how often the derived pair is
    the true one."""
    score = collections.defaultdict(lambda: [0, 0])
    for c in conns:
        cands = set(true_pair(c, truths))
        for e in c["events"]:
            ev = f"{STATE.get(e['old'])}->{STATE.get(e['new'])}"
            for name, p in derive(e, c["role"]).items():
                s = score[(c["mode"], c["role"], ev, name)]
                s[0] += p in cands
                s[1] += 1
    out.append("ISN derivation vs tcpdump (ok/total) per mode, role, "
               "event, method:")
    for k in sorted(score):
        ok, n = score[k]
        out.append(f"  {k[0]:5} {k[1]:6} {k[2]:24} {k[3]:7} {ok}/{n}")


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
    conntrack reply; ambiguity counted where an original tuple maps to
    more than one inserted entry."""
    inserts = collections.defaultdict(list)
    for ct in cts:
        inserts[ct["orig"]].append(ct)
    ok = amb = miss = right = 0
    for c in (c for c in conns if c["role"] == "client"):
        hits = inserts.get(f"{c['src']}>{c['dst']}", [])
        ok += len(hits) == 1
        amb += len(hits) > 1
        miss += not hits
        p = c.get("isn_peer")
        if len(hits) >= 1 and p:
            right += hits[-1]["reply"] == f"{p['src']}>{p['dst']}"
    out.append(f"conntrack inserts (confirm events in nat): {len(cts)}")
    out.append(f"conntrack-insert join: unique={ok} ambiguous={amb} "
               f"missing={miss}; reply tuple agrees with ISN join={right}")
    out.append(f"/proc/net/nf_conntrack at end: {len(dump)} entries "
               f"(states {dict(collections.Counter(r[2] for r in dump))})")
    seq_ok = sum(1 for c in conns if c["role"] == "client" and
                 c.get("isn_peer") and any(
                     (int(ct["td_end0"]) - 1) % M32 == key_at_owner(c)[0]
                     for ct in inserts.get(f"{c['src']}>{c['dst']}", [])))
    out.append(f"conntrack td_end0-1 == client iss (non-TFO expected): "
               f"{seq_ok}")


def tag_modes(conns):
    """Labels connections by workload phase: reuse uses source port 45678,
    TFO is the last 6 client connections (rig.sh runs plain, reuse, tfo in
    that order); servers inherit the label through the ISN join later."""
    clients = [c for c in conns if c["role"] == "client"]
    for c in conns:
        c["mode"] = "?"
    for i, c in enumerate(clients):
        c["mode"] = ("reuse" if c["src"].endswith(":45678") else
                     "tfo" if i >= len(clients) - 6 else "plain")


def tfo_evidence(d, out):
    """Counts SYNs that carried payload on the client side: proof the TFO
    phase really put data in the SYN."""
    n = sum(1 for line in open(f"{d}/hostA.syn.txt")
            if "Flags [S]," in line and not line.rstrip().endswith(
                "length 0"))
    out.append(f"client SYNs with payload (TFO): {n}")


def main(d):
    events, cts = load_events(f"{d}/isn.log")
    conns = connections(events)
    tag_modes(conns)
    truths = {"A": truth(f"{d}/hostA.syn.txt"),
              "B": truth(f"{d}/hostB.syn.txt")}
    out = []
    isn_join(conns, out)
    for c in conns:
        if c["role"] == "client" and c["isn_peer"]:
            c["isn_peer"]["mode"] = c["mode"]
    check_methods(conns, truths, out)
    ct_join(conns, cts, ct_dump(f"{d}/conntrack.txt"), out)
    tfo_evidence(d, out)
    print("\n".join(out))


if __name__ == "__main__":
    main(sys.argv[1])
