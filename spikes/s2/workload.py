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
    closes.

client HOST PORT MODE COUNT
    MODE plain: COUNT connections with varying request sizes, normal close.
    MODE reuse: COUNT connections all from the same source port, closed with
        SO_LINGER 0 (RST, no TIME_WAIT) so the port is free at once - the
        4-tuple repeats within milliseconds, the ISN pair must not.
    MODE tfo:   COUNT connections with MSG_FASTOPEN; the first one fetches the
        cookie, the rest carry the request in the SYN.
"""

import socket
import sys
import time

ACCEPT_DELAY = 0.05
SIZES = [0, 1, 100, 1460, 5000, 65536]
REUSE_PORT = 45678


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
            n = 0
            while True:
                chunk = conn.recv(65536)
                if not chunk:
                    break
                n += len(chunk)
            conn.sendall(b"r" * min(n, 4096))


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


def reuse(host, port, count):
    for i in range(count):
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind(("0.0.0.0", REUSE_PORT))
        s.connect((host, port))
        _exchange(s, SIZES[i % len(SIZES)])
        # SO_LINGER {on, 0}: close sends RST, the port skips TIME_WAIT.
        s.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER,
                     b"\x01\x00\x00\x00\x00\x00\x00\x00")
        s.close()


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
    {"plain": plain, "reuse": reuse, "tfo": tfo}[mode](host, port, count)


if __name__ == "__main__":
    main(sys.argv)
