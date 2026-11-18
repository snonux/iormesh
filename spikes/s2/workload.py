#!/usr/bin/env python3
"""S2 spike (throwaway, task 6b4): TCP client and server for the rig.

server PORT
    Listens on 0.0.0.0:PORT with TCP_FASTOPEN enabled (effective only when
    the namespace's net.ipv4.tcp_fastopen sysctl allows it). Each accept is
    delayed by ACCEPT_DELAY so the request bytes - and often the client's
    FIN - are already queued when accept() returns: that is the case where
    iormesh first learns the passive socket's owner and has to rebuild the
    ISN pair from bytes_received (docs/implementation.md, S2 experiment 1).
    Reads to EOF, answers with as many bytes as the request had (capped),
    closes. A request starting with PING_BYTE is an echo session instead:
    every chunk is sent straight back until EOF.

client HOST PORT MODE COUNT
    MODE plain: COUNT connections with varying request sizes, normal close.
    MODE reuse-tw: COUNT connections all from source port 45678 with the
        normal FIN close. The client closes first, so each connection ends
        in TIME_WAIT on the client; the next connect from the same port
        takes the tuple over through the kernel's TIME_WAIT reuse
        (tcp_twsk_unique), which sets the new ISN to the old connection's
        snd_nxt + 65535 + 2 instead of drawing it from the clock. No RST is
        sent (setting SO_LINGER 0 after the FIN exchange, as the first
        version did, changes nothing).
    MODE reuse-rst: COUNT connections all from source port 45679, aborted
        with SO_LINGER 0 right after the request (no shutdown), so close()
        sends an RST and leaves no TIME_WAIT; the next connect gets a fresh
        clock-based ISN for the same tuple.
    MODE pingpong: COUNT connections of PING_ROUNDS request/response rounds
        each (PING_SIZE bytes echoed back). Added for the clock bounds
        (task 7b4): a single request/response leaves the request queued
        behind the server's accept delay, so its send->receive gap is 50 ms;
        later rounds find the peer already blocked in recv, which is what
        tightens the bound in both directions.
    MODE tfo:   COUNT connections with MSG_FASTOPEN; the first one fetches the
        cookie, the rest carry the request in the SYN.
"""

import socket
import sys
import time

ACCEPT_DELAY = 0.05
SIZES = [0, 1, 100, 1460, 5000, 65536]
REUSE_TW_PORT = 45678
REUSE_RST_PORT = 45679
LINGER_0 = b"\x01\x00\x00\x00\x00\x00\x00\x00"
PING_BYTE = b"P"
PING_SIZE = 32
PING_ROUNDS = 20


def serve(port):
    ls = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    ls.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    ls.setsockopt(socket.IPPROTO_TCP, socket.TCP_FASTOPEN, 16)
    ls.bind(("0.0.0.0", port))
    ls.listen(64)
    print("ready", flush=True)
    while True:
        time.sleep(ACCEPT_DELAY)
        conn, _ = ls.accept()
        with conn:
            try:
                _answer(conn)
            except (ConnectionResetError, BrokenPipeError):
                pass  # reuse-rst clients abort before the answer


def _answer(conn):
    first = conn.recv(65536)
    if first.startswith(PING_BYTE):
        _echo(conn, first)
        return
    n, chunk = len(first), first
    while chunk:
        chunk = conn.recv(65536)
        n += len(chunk)
    conn.sendall(b"r" * min(n, 4096))


def _echo(conn, chunk):
    while chunk:
        conn.sendall(chunk)
        chunk = conn.recv(65536)


def _exchange(s, size, sent=0):
    """Sends the rest of the request, half-closes, reads the answer."""
    if size > sent:
        s.sendall(b"q" * (size - sent))
    s.shutdown(socket.SHUT_WR)
    while s.recv(65536):
        pass


def plain(host, port, count):
    for i in range(count):
        with socket.create_connection((host, port)) as s:
            _exchange(s, SIZES[i % len(SIZES)])


def _bound(port):
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("0.0.0.0", port))
    return s


def reuse_tw(host, port, count):
    for i in range(count):
        with _bound(REUSE_TW_PORT) as s:
            s.connect((host, port))
            _exchange(s, SIZES[i % len(SIZES)])


def reuse_rst(host, port, count):
    for i in range(count):
        s = _bound(REUSE_RST_PORT)
        s.connect((host, port))
        s.sendall(b"q" * SIZES[i % len(SIZES)])
        # SO_LINGER {on, 0} before any FIN: close() aborts with an RST.
        s.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, LINGER_0)
        s.close()
        time.sleep(0.1)  # let the server drain its accept before the next


def pingpong(host, port, count):
    for _ in range(count):
        with socket.create_connection((host, port)) as s:
            for _ in range(PING_ROUNDS):
                s.sendall(PING_BYTE * PING_SIZE)
                got = 0
                while got < PING_SIZE:
                    got += len(s.recv(PING_SIZE - got))
            _exchange(s, 0)


def tfo(host, port, count):
    for i in range(count):
        size = 100 + i
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sent = s.sendto(b"q" * size, socket.MSG_FASTOPEN, (host, port))
        _exchange(s, size, sent)
        s.close()


def main(argv):
    if argv[1] == "server":
        serve(int(argv[2]))
        return
    host, port, mode, count = argv[2], int(argv[3]), argv[4], int(argv[5])
    {"plain": plain, "reuse-tw": reuse_tw, "reuse-rst": reuse_rst,
     "pingpong": pingpong, "tfo": tfo}[mode](host, port, count)


if __name__ == "__main__":
    main(sys.argv)
