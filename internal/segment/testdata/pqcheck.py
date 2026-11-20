"""Read an events segment with pyarrow and DuckDB, independently of
parquet-go, and compare it with the NDJSON of the same events (task ee4,
TestIndependentReaders). Usage: pqcheck.py EVENTS.parquet EVENTS.ndjson.
Prints one OK line per check; exits non-zero on the first mismatch."""

import json
import sys

import duckdb
import pyarrow.parquet as pq

# Optional non-pointer fields: parquet stores their zero value, NDJSON
# omits it (omitempty). By dotted path.
ZERO_OMITTED = {
    "err", "conn_id", "java.thread", "java.carrier", "http.method",
    "http.scheme", "http.host", "http.path", "http.status", "http.req_seq",
    "http.version",
}

# The pointer (nullable) leaves and objects whose IS NULL DuckDB must
# answer as NDJSON's absence does.
NULLABLE_ARGS = [
    "path", "new_path", "flags", "cmd", "offset", "local_addr", "local_port",
    "remote_addr", "remote_port", "sock_state", "marker", "lost",
    "drop_reason", "end_reason", "fd2", "fd2_gen", "signal", "target_pid",
    "sender_pid", "count", "snd_una", "rcv_nxt", "bytes_acked",
    "bytes_received", "tcp_seq",
]
NULLABLE_TOP = ["stack_id", "java", "http", "session", "otel", "dns"]


def norm(v, path=""):
    """Drops what NDJSON cannot show: nulls, empty lists, and the zero
    value of an omitempty non-pointer field."""
    if isinstance(v, dict):
        out = {}
        for k, x in v.items():
            p = f"{path}.{k}" if path else k
            if x is None or x == []:
                continue
            if p in ZERO_OMITTED and x in ("", 0, False):
                continue
            out[k] = norm(x, p)
        return out
    if isinstance(v, list):
        return [norm(x, path) for x in v]
    return v


def compare(name, rows, want):
    if len(rows) != len(want):
        sys.exit(f"{name}: {len(rows)} rows, want {len(want)}")
    for i, (r, w) in enumerate(zip(rows, want)):
        if norm(r) != norm(w):
            sys.exit(f"{name}: row {i}:\n got  {norm(r)}\n want {norm(w)}")
    print(f"OK {name}: {len(rows)} rows equal the NDJSON")


def absent_in(w, top, leaf):
    if leaf:
        return leaf not in w.get(top, {})
    return top not in w


def null_checks(con, path, want):
    n = 0
    for col in [f"args.{a}" for a in NULLABLE_ARGS] + NULLABLE_TOP:
        top, _, leaf = col.partition(".")
        absent = sum(1 for w in want if absent_in(w, top, leaf))
        expr = f"{top}.{leaf}" if leaf else top
        q = f"SELECT count(*) FROM read_parquet('{path}') WHERE {expr} IS NULL"
        got = con.execute(q).fetchone()[0]
        if got != absent:
            sys.exit(f"duckdb: {expr} IS NULL: {got} rows, want {absent}")
        n += 1
    print(f"OK duckdb IS NULL: {n} columns match NDJSON absence")


def main():
    path, ndjson = sys.argv[1], sys.argv[2]
    with open(ndjson) as f:
        want = [json.loads(line) for line in f]
    compare("pyarrow", pq.read_table(path).to_pylist(), want)
    con = duckdb.connect()
    cur = con.execute(f"SELECT * FROM read_parquet('{path}') ORDER BY seq")
    cols = [d[0] for d in cur.description]
    compare("duckdb", [dict(zip(cols, r)) for r in cur.fetchall()], want)
    null_checks(con, path, want)


main()
