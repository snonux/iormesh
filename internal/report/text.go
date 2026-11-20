package report

import (
	"fmt"
	"io"
	"strings"

	"iormesh/internal/termsafe"
)

// The text rendering, what `iormeshctl show report` prints after the
// run summary and what the LLM eval hands the model: one block per
// process, one line per file, connection, thread call and method, each
// line a pure function of the Report so text goldens work. Every string
// from the traces goes through termsafe.String (a compromised server
// could put escape sequences in a path, a comm or a request line); the
// JSON (report.json, -json) stays as it is, JSON escapes them anyway.

// WriteText prints the report.
func WriteText(w io.Writer, r Report) error {
	var b strings.Builder
	if r.Untracked > 0 {
		fmt.Fprintf(&b, "partial    %d events past the rollup's tracking cap are not in it\n", r.Untracked)
	}
	for i, p := range r.Processes {
		if i > 0 {
			b.WriteString("\n")
		}
		writeProcess(&b, p)
	}
	if r.MoreProcesses > 0 {
		fmt.Fprintf(&b, "\n... %d more processes\n", r.MoreProcesses)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeProcess prints one process block.
func writeProcess(b *strings.Builder, p Process) {
	s := termsafe.String
	fmt.Fprintf(b, "process    %s/%s/%s[%d] %s (%s)\n", s(p.Project), s(p.Host), s(p.Comm), p.PID,
		s(p.FQDN), s(p.ID))
	if p.App != "" {
		fmt.Fprintf(b, "  app      %s\n", s(p.App))
	}
	if len(p.Argv) > 0 {
		fmt.Fprintf(b, "  argv     %s\n", s(strings.Join(p.Argv, " ")))
	}
	if len(p.Serves) > 0 {
		fmt.Fprintf(b, "  serves   %s\n", s(strings.Join(p.Serves, ", ")))
	}
	fmt.Fprintf(b, "  events   %d, %d with a Java stack\n", p.Events, p.JavaEvents)
	for _, f := range p.Files {
		fmt.Fprintf(b, "  file     %s\n", fileLine(f))
	}
	more(b, "files", p.MoreFiles)
	for _, c := range p.Conns {
		fmt.Fprintf(b, "  conn     %s\n", connLine(c))
	}
	more(b, "connections", p.MoreConns)
	for _, t := range p.Threads {
		writeThread(b, t)
	}
	more(b, "threads", p.MoreThreads)
	for _, m := range p.Methods {
		fmt.Fprintf(b, "  method   %s: %d events, %d B\n", s(m.Method), m.Events, m.Bytes)
	}
	more(b, "methods", p.MoreMethods)
}

// fileLine is "PATH: read Nx, B B read; written Nx, B B written; OPS;
// by METHODS".
func fileLine(f File) string {
	s := termsafe.String
	var parts []string
	if f.ReadOpens > 0 || f.ReadBytes > 0 {
		parts = append(parts, fmt.Sprintf("opened for reading %dx, %d B read", f.ReadOpens, f.ReadBytes))
	}
	if f.WriteOpens > 0 || f.WriteBytes > 0 {
		parts = append(parts, fmt.Sprintf("opened for writing %dx, %d B written", f.WriteOpens, f.WriteBytes))
	}
	if len(f.Ops) > 0 {
		ops := s(strings.Join(f.Ops, ", "))
		if f.MoreOps > 0 {
			ops += fmt.Sprintf(" (+%d more)", f.MoreOps)
		}
		parts = append(parts, ops)
	}
	if len(f.Methods) > 0 {
		parts = append(parts, "by "+s(strings.Join(f.Methods, ", ")))
	}
	return s(f.Path) + ": " + strings.Join(parts, "; ")
}

// connLine is "ID ROLE LOCAL -> REMOTE, service S, peer P; sent, received;
// N requests".
func connLine(c Conn) string {
	s := termsafe.String
	line := fmt.Sprintf("%s %s %s -> %s, service %s, %s", s(c.ID), dash(c.Role), dash(s(c.Local)),
		dash(s(c.Remote)), dash(s(c.Service)), peerText(c.PeerState, c.Peers))
	line += fmt.Sprintf("; sent %d B, received %d B", c.SentBytes, c.RecvBytes)
	if n := len(c.Requests) + c.MoreRequests; n > 0 {
		line += fmt.Sprintf("; %d requests", n)
	}
	return line
}

// writeThread prints a thread and its calls, one line each.
func writeThread(b *strings.Builder, t Thread) {
	s := termsafe.String
	name := s(t.Name)
	if t.Name != "tid "+itoa(t.TID) {
		name += " (tid " + itoa(t.TID) + ")"
	}
	fmt.Fprintf(b, "  thread   %s: %d events, %d B\n", name, t.Events, t.Bytes)
	for _, c := range t.Calls {
		fmt.Fprintf(b, "    %-4s   %s %s (%s) -> %d, service %s, %s (conn %s)\n", dash(c.Dir), s(c.Method),
			s(c.Path), dash(s(c.Scheme)), c.Status, dash(s(c.Service)), peerText(peerState(c.Peers), c.Peers), s(c.Conn))
	}
	if t.MoreCalls > 0 {
		fmt.Fprintf(b, "    ... %d more calls\n", t.MoreCalls)
	}
}

func peerState(peers []string) string {
	if len(peers) > 0 {
		return PeerTraced
	}
	return PeerUntraced
}

// peerText is "peer A, B" or "peer untraced".
func peerText(state string, peers []string) string {
	if state != PeerTraced || len(peers) == 0 {
		return "peer untraced"
	}
	return "peer " + termsafe.String(strings.Join(peers, ", "))
}

func more(b *strings.Builder, what string, n int) {
	if n > 0 {
		fmt.Fprintf(b, "  ...      %d more %s\n", n, what)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
