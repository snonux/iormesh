#!/usr/bin/env python3
"""Generates the static documentation diagrams for iormesh.

Usage: python3 docs/diagrams/gen.py docs/diagrams

The SVGs use presentation attributes only (no CSS, no scripts, no external
fonts), so they render the same in browsers, GitHub's image proxy and librsvg.
Palette follows assets/logo.svg.
"""
import sys
from html import escape

BG, PANEL, BOX = "#0b1026", "#131a3d", "#1b2454"
LINE, TEXT, MUTED = "#3a4a86", "#e6edf7", "#9fb0d0"
CYAN, ROSE, AMBER = "#22d3ee", "#fb7185", "#fbbf24"
SANS = "'Inter','Segoe UI','Noto Sans',Helvetica,Arial,sans-serif"
MONO = "'JetBrains Mono','Noto Sans Mono',Menlo,Consolas,monospace"
W = 1000


class Svg:
    """Collects SVG elements; every helper appends one element."""

    def __init__(self, height, title, desc):
        self.h, self.title, self.desc, self.out = height, title, desc, []

    def rect(self, x, y, w, h, fill=BOX, stroke=LINE, rx=10, sw=1.5, dash=None):
        d = f' stroke-dasharray="{dash}"' if dash else ""
        self.out.append(
            f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" '
            f'fill="{fill}" stroke="{stroke}" stroke-width="{sw}"{d}/>')

    def text(self, x, y, s, size=13, fill=TEXT, anchor="start", bold=False,
             mono=False):
        weight = ' font-weight="600"' if bold else ""
        self.out.append(
            f'<text x="{x}" y="{y}" font-family="{MONO if mono else SANS}" '
            f'font-size="{size}" fill="{fill}" text-anchor="{anchor}"{weight}>'
            f'{escape(s)}</text>')

    def path(self, d, color=CYAN, dash=None, arrow=True, sw=2):
        da = f' stroke-dasharray="{dash}"' if dash else ""
        end = f' marker-end="url(#a{color[1:]})"' if arrow else ""
        self.out.append(
            f'<path d="{d}" fill="none" stroke="{color}" stroke-width="{sw}" '
            f'stroke-linecap="round" stroke-linejoin="round"{da}{end}/>')

    def arrow(self, x1, y1, x2, y2, color=CYAN, dash=None):
        self.path(f"M{x1} {y1} L{x2} {y2}", color, dash)

    def header(self, title, subtitle):
        self.text(40, 46, title, 22, bold=True)
        self.text(40, 70, subtitle, 13, MUTED)

    def render(self):
        markers = "".join(
            f'<marker id="a{c[1:]}" viewBox="0 0 10 10" refX="8.5" refY="5" '
            f'markerWidth="7" markerHeight="7" orient="auto-start-reverse">'
            f'<path d="M0 0 L10 5 L0 10 z" fill="{c}"/></marker>'
            for c in (CYAN, ROSE, AMBER, MUTED))
        body = "\n  ".join(self.out)
        return (
            f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" '
            f'height="{self.h}" viewBox="0 0 {W} {self.h}" role="img" '
            f'aria-labelledby="t d">\n'
            f'  <title id="t">{escape(self.title)}</title>\n'
            f'  <desc id="d">{escape(self.desc)}</desc>\n'
            f'  <defs>{markers}</defs>\n'
            f'  <rect width="{W}" height="{self.h}" rx="18" fill="{BG}"/>\n'
            f'  {body}\n</svg>\n')


def server(s, x0, name, target, with_agent):
    """One traced host: target process, kernel probes, tracer."""
    s.rect(x0, 96, 440, 336, PANEL, LINE, 14)
    s.text(x0 + 16, 120, name, 12, MUTED, bold=True)
    s.rect(x0 + 20, 134, 400, 104)
    s.text(x0 + 36, 160, target, 14, bold=True)
    s.text(x0 + 36, 180, "application threads", 11, MUTED)
    if with_agent:
        s.rect(x0 + 210, 150, 194, 72, PANEL, ROSE)
        s.text(x0 + 222, 174, "Java agent", 13, bold=True)
        s.text(x0 + 222, 192, "hooks at JDK I/O boundaries", 11, MUTED)
        s.text(x0 + 222, 208, "one stack per I/O op", 11, MUTED)
        s.arrow(x0 + 350, 222, x0 + 350, 338, ROSE)
        s.text(x0 + 360, 278, "spool", 11, ROSE)
        s.text(x0 + 360, 293, "(no socket)", 11, ROSE)
    else:
        s.text(x0 + 222, 180, "not a JVM: no agent,", 11, MUTED)
        s.text(x0 + 222, 196, "events marked unattributed", 11, MUTED)
    s.rect(x0 + 20, 262, 270, 56)
    s.text(x0 + 36, 286, "Linux kernel · eBPF probes", 13, bold=True)
    s.text(x0 + 36, 304, "syscalls · socket state · JDK uprobes", 11, MUTED)
    s.rect(x0 + 20, 340, 400, 76, BOX, CYAN)
    s.text(x0 + 36, 364, "iormesh tracer (correlator)", 13, bold=True)
    s.text(x0 + 36, 383, "joins fd ↔ path ↔ socket ↔ Java stack", 11, MUTED)
    s.text(x0 + 36, 402, "traces/<project>/ready/*.parquet · 1 per minute", 11,
           MUTED, mono=True)
    s.arrow(x0 + 110, 238, x0 + 110, 260)
    s.text(x0 + 120, 254, "syscalls", 11, MUTED)
    s.arrow(x0 + 110, 318, x0 + 110, 338)
    s.text(x0 + 120, 333, "ring buffer", 11, MUTED)
    # down: trace data; up: control. Both over SSH from the controller.
    s.arrow(x0 + 60, 434, x0 + 60, 486)
    s.text(x0 + 70, 465, "segments (rsync)", 11, CYAN)
    s.arrow(x0 + 230, 486, x0 + 230, 434, ROSE)
    s.text(x0 + 240, 465, "deploy · start · stop (SSH)", 11, ROSE)


def architecture():
    s = Svg(680, "How iormesh works",
            "Two servers each run a tracer made of eBPF probes and a "
            "correlator, optionally fed by a Java agent. The controller "
            "collects trace segments over SSH and merges them into one graph.")
    s.header("How iormesh works",
             "Tracers on the servers write trace segments. Everything "
             "cross-host happens on the controller.")
    server(s, 40, "SERVER A", "target process (JVM)", True)
    server(s, 520, "SERVER B", "peer process", False)
    s.arrow(462, 200, 538, 200, AMBER)
    s.text(500, 190, "TCP", 12, AMBER, "middle", bold=True)
    s.rect(40, 488, 920, 164, PANEL, LINE, 14)
    s.text(56, 512, "CONTROLLER (YOUR LAPTOP) · iormeshctl", 12, MUTED,
           bold=True)
    boxes = [
        ("collect", "rsync the ready/ segments,", "delete after transfer"),
        ("merge", "fuse both halves of a", "connection into one edge"),
        ("graph + service db", "who talks to whom,", "with named services"),
        ("views + query", "SVG · sequence diagram ·", "slices for humans and LLMs"),
    ]
    for i, (title, l1, l2) in enumerate(boxes):
        x = 60 + 220 * i
        s.rect(x, 526, 200, 78, BOX, CYAN)
        s.text(x + 14, 550, title, 13, bold=True)
        s.text(x + 14, 570, l1, 11, MUTED)
        s.text(x + 14, 586, l2, 11, MUTED)
        if i:
            s.arrow(x - 19, 565, x - 2, 565)
    s.text(56, 634, "The only way in is the operator's SSH key — tracers "
           "listen on nothing and connect to nothing.", 11, MUTED)
    return s


def workflow():
    s = Svg(480, "Operator workflow",
            "add, deploy, trace, collect, merge and explore; collect repeats "
            "while the session runs; expand feeds new hosts back into deploy; "
            "rm cleans up.")
    s.header("Operator workflow",
             "Everything is driven from the laptop with iormeshctl. Servers "
             "only ever run the tracer.")
    steps = [
        ("add", "add HOST", "enroll a server", CYAN),
        ("deploy", "deploy", "push + start tracer", CYAN),
        ("trace", "≤ 4h, disk-bounded", "1 segment per minute", ROSE),
        ("collect", "collect", "rsync the segments", CYAN),
        ("merge", "(part of collect)", "fuse cross-host edges", CYAN),
        ("explore", "query · svg", "views, sequence, LLM", CYAN),
    ]
    for i, (title, cmd, note, color) in enumerate(steps):
        x = 30 + 160 * i
        s.rect(x, 150, 140, 84, BOX, color)
        s.text(x + 14, 177, title, 15, bold=True)
        s.text(x + 126, 174, str(i + 1), 11, MUTED, "end")
        s.text(x + 14, 199, cmd, 11, color, mono=True)
        s.text(x + 14, 218, note, 11, MUTED)
        if i:
            s.arrow(x - 19, 192, x - 2, 192, MUTED)
    # collect repeats while the tracer is still running
    s.path("M580 148 C580 112 420 112 420 146", CYAN, "5 4")
    s.text(500, 108, "repeat while the session runs", 11, CYAN, "middle")
    # expansion loop: unresolved edges become new hosts
    s.rect(330, 300, 300, 104, BOX, CYAN)
    s.text(344, 327, "expand", 15, bold=True)
    s.text(616, 324, "7", 11, MUTED, "end")
    s.text(344, 349, "expand --apply --hops N", 11, CYAN, mono=True)
    s.text(344, 369, "untraced peers become new hosts", 11, MUTED)
    s.text(344, 386, "allowlist · hop limit · node cap", 11, MUTED)
    s.path("M740 236 L740 352 L633 352", CYAN, "5 4")
    s.text(732, 290, "unresolved edges", 11, CYAN, "end")
    s.path("M330 352 L260 352 L260 237", CYAN, "5 4")
    s.text(252, 290, "next round", 11, CYAN, "end")
    # teardown
    s.rect(830, 300, 140, 104, BOX, CYAN)
    s.text(844, 327, "rm", 15, bold=True)
    s.text(956, 324, "8", 11, MUTED, "end")
    s.text(844, 349, "rm HOST", 11, CYAN, mono=True)
    s.text(844, 369, "sync, stop, remove", 11, MUTED)
    s.text(844, 386, "/opt/iormesh", 11, MUTED, mono=True)
    s.arrow(900, 236, 900, 298, MUTED)
    s.text(908, 272, "when done", 11, MUTED)
    s.rect(40, 440, 12, 12, BOX, CYAN, 3)
    s.text(60, 451, "runs on your laptop", 11, MUTED)
    s.rect(200, 440, 12, 12, BOX, ROSE, 3)
    s.text(220, 451, "runs on the servers", 11, MUTED)
    return s


def roadmap():
    s = Svg(420, "Roadmap",
            "Milestones M0 to M7 in execution order. Spike S3 gates M2, "
            "S1 gates M3 and S2 gates M4.")
    s.header("Roadmap",
             "Milestones in execution order. Spikes prove the risky "
             "mechanisms before the milestone that depends on them.")
    ms = [
        ("M0", "workload", "framework", "markers and", "expectations"),
        ("M1", "walking", "skeleton", "one fused edge", "on loopback"),
        ("M2", "full trace", "surface", "files, sockets,", "IPC, restarts"),
        ("M3", "Java agent", "", "I/O ↔ Java stack,", "HTTP, OTEL"),
        ("M4", "controller", "", "deploy, collect,", "cross-host merge"),
        ("M5", "views, query,", "LLM eval", "SVG, sequence", "diagram"),
        ("M6", "mesh", "expansion", "follow edges", "to new hosts"),
        ("M7", "Kubernetes", "", "sidecar, pods,", "ingress"),
    ]
    for i, (mid, n1, n2, o1, o2) in enumerate(ms):
        x = 26 + 120 * i
        s.rect(x, 196, 108, 112, BOX, CYAN)
        s.text(x + 12, 222, mid, 16, CYAN, bold=True)
        s.text(x + 12, 243, n1, 12.5, bold=True)
        s.text(x + 12, 259, n2, 12.5, bold=True)
        s.text(x + 12, 281, o1, 10.5, MUTED)
        s.text(x + 12, 295, o2, 10.5, MUTED)
    s.arrow(26, 330, 974, 330, MUTED)
    s.text(500, 350, "execution order", 11, MUTED, "middle")
    spikes = [
        (320, "S3", "overhead & volume"),
        (440, "S1", "Java ↔ syscall join"),
        (560, "S2", "cross-host join"),
    ]
    for cx, sid, name in spikes:
        s.rect(cx - 56, 106, 112, 50, BOX, AMBER, 25)
        s.text(cx, 127, sid, 13, AMBER, "middle", bold=True)
        s.text(cx, 144, name, 10.5, TEXT, "middle")
        s.arrow(cx, 158, cx, 194, AMBER)
    s.text(330, 180, "gates", 11, AMBER)
    s.rect(40, 384, 12, 12, BOX, CYAN, 3)
    s.text(60, 395, "milestone — done when its PLAN.md acceptance holds", 11,
           MUTED)
    s.rect(420, 384, 12, 12, BOX, AMBER, 6)
    s.text(440, 395, "spike — time-boxed, ends with a written result; each "
           "milestone lands its scenarios first", 11, MUTED)
    return s


def lane(s, y, label, agent_y, bpf_y):
    """One host/thread lane with an agent row and an eBPF row."""
    s.text(40, y - 10, label, 13, bold=True)
    s.text(960, y - 10, "time →", 11, MUTED, "end")
    s.rect(40, y, 920, 124, PANEL, LINE, 14)
    s.text(56, agent_y + 18, "Java agent", 11, MUTED)
    s.text(56, bpf_y + 18, "eBPF", 11, MUTED)


def bracket(s, x, y, w, label):
    s.rect(x, y, w, 28, PANEL, CYAN, 6)
    s.text(x + w / 2, y + 18, label, 11, TEXT, "middle")


def chip(s, x, y, w, label):
    s.rect(x, y, w, 28, BOX, LINE, 6)
    s.text(x + w / 2, y + 18, label, 10.5, TEXT, "middle", mono=True)


def join():
    s = Svg(590, "Two joins make one story",
            "A Java stack is joined to the syscalls of the same thread "
            "while its I/O hook is open; the two hosts' views of one TCP "
            "connection are joined into one edge; order is happens-before.")
    s.header("Two joins make one story",
             "How one request from gateway to orders becomes an attributed, "
             "ordered line in the sequence view.")
    lane(s, 124, "host A · gateway · thread http-1", 142, 202)
    bracket(s, 150, 142, 170, "OrdersClient.fetch()")
    chip(s, 185, 202, 100, "write fd 9")
    s.path("M235 170 L235 200", CYAN, "3 3", arrow=False, sw=1.5)
    bracket(s, 770, 142, 170, "OrdersClient.fetch()")
    chip(s, 805, 202, 100, "read fd 9")
    s.path("M855 170 L855 200", CYAN, "3 3", arrow=False, sw=1.5)
    s.text(336, 154, "① Java ↔ kernel — the syscall runs on the same thread,",
           11.5, CYAN)
    s.text(353, 170, "while that thread's I/O hook is open, so it gets that stack.",
           11.5, CYAN)

    lane(s, 330, "host B · orders · thread worker-3", 408, 348)
    chip(s, 350, 348, 100, "read fd 12")
    chip(s, 478, 348, 134, "openat orders.conf")
    chip(s, 640, 348, 100, "write fd 12")
    bracket(s, 330, 408, 140, "OrderHandler.handle()")
    bracket(s, 478, 408, 134, "Config.load()")
    bracket(s, 620, 408, 140, "OrderHandler.handle()")
    for x in (400, 545, 690):
        s.path(f"M{x} 378 L{x} 406", CYAN, "3 3", arrow=False, sw=1.5)

    s.path("M235 232 L392 345", AMBER)
    s.path("M698 345 L850 233", AMBER)
    s.text(545, 276, "② host ↔ host — both tracers recorded the same TCP",
           11.5, AMBER, "middle")
    s.text(545, 292, "connection, so its two halves fuse into one edge.",
           11.5, AMBER, "middle")

    s.text(40, 492, "③ Order is happens-before: each thread's own order plus "
           "the send → receive arrows. Clocks are never compared across "
           "hosts.", 11.5, MUTED)
    s.rect(40, 510, 920, 56, BOX, CYAN)
    s.text(56, 532, "SEQUENCE VIEW", 10.5, MUTED, bold=True)
    s.text(56, 552, "gateway http-1 → orders worker-3: read request → read "
           "orders.conf → wrote response → gateway http-1", 11.5, TEXT,
           mono=True)
    return s


def main(outdir):
    for name, build in (("architecture", architecture), ("workflow", workflow),
                        ("roadmap", roadmap), ("join", join)):
        with open(f"{outdir}/{name}.svg", "w", encoding="utf-8") as f:
            f.write(build().render())


if __name__ == "__main__":
    main(sys.argv[1])
