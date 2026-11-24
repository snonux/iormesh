package viewmodel

import (
	"fmt"
	"io"
	"strings"

	"iormesh/internal/report"
)

// WriteProcesses prints the processes view (`show processes`): one block
// per process incarnation, grouped by host as the model sorts them —
// header, restarts, threads (a carrier's virtual-thread I/O counted
// apart), files and sockets. A rollup past its tracking cap is said to
// be partial first, as report.WriteText does, so the list never reads
// as complete.
func WriteProcesses(w io.Writer, m Model) error {
	m = safeModel(m)
	var b strings.Builder
	if l := PartialLine(m); l != "" {
		b.WriteString(l + "\n\n")
	}
	for i, p := range m.Processes {
		if i > 0 {
			b.WriteString("\n")
		}
		writeProcess(&b, p)
	}
	if m.MoreProcesses > 0 {
		fmt.Fprintf(&b, "\ntruncated: %d more processes\n", m.MoreProcesses)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// PartialLine says the processes list is partial when the rollup hit
// its tracking cap (Model.Untracked), "" when it is complete. `show
// processes` and the TUI's process detail print it; the site's
// processes screen words the same from the model's untracked.
func PartialLine(m Model) string {
	if m.Untracked == 0 {
		return ""
	}
	return fmt.Sprintf("partial    %d events past the rollup's tracking cap are not in it", m.Untracked)
}

// WriteProcess prints one process block, as `show processes` does: the
// TUI's process screen shows the same text for the selected process.
func WriteProcess(w io.Writer, p Process) error {
	var b strings.Builder
	writeProcess(&b, safeProcess(p))
	_, err := io.WriteString(w, b.String())
	return err
}

// writeProcess prints one process block.
func writeProcess(b *strings.Builder, p Process) {
	fmt.Fprintf(b, "process    %s/%s/%s[%d] incarnation %d  %s  (as a client: %s)\n", p.Project,
		p.Host, p.Comm, p.PID, p.Incarnation, p.FQDN, p.Service)
	if p.Incarnations > 1 {
		fmt.Fprintf(b, "  pid      %d incarnations of pid %d on this host (restart or reused pid)\n", p.Incarnations, p.PID)
	}
	if c := ContainerLabel(p); c != "" {
		fmt.Fprintf(b, "  container %s\n", c)
	}
	if len(p.Serves) > 0 {
		fmt.Fprintf(b, "  serves   %s\n", strings.Join(p.Serves, ", "))
	}
	fmt.Fprintf(b, "  events   %d, %d with a Java stack\n", p.Events, p.JavaEvents)
	for _, t := range p.Threads {
		fmt.Fprintf(b, "  thread   %s\n", procThreadLine(t))
	}
	moreLine(b, "threads", p.MoreThreads)
	for _, f := range p.Files {
		fmt.Fprintf(b, "  file     %s\n", procFileLine(f))
	}
	moreLine(b, "files", p.MoreFiles)
	for _, s := range p.Sockets {
		fmt.Fprintf(b, "  socket   %s\n", socketLine(s))
	}
	moreLine(b, "sockets", p.MoreSockets)
}

// containerIDShort is how many hex digits of a named container's id the
// label keeps: enough to tell runs apart, as `crictl ps` shows them.
const containerIDShort = 12

// ContainerLabel is how the surfaces show a process's container (task
// jk4): "NAME (ID12)" when the pod's container map names it, the whole
// id when it does not (a session collected before the map), "" outside
// a container.
func ContainerLabel(p Process) string {
	switch {
	case p.ContainerID == "":
		return ""
	case p.Container == "":
		return p.ContainerID
	}
	return p.Container + " (" + p.ContainerID[:min(containerIDShort, len(p.ContainerID))] + ")"
}

// procThreadLine is "NAME (tid N): E events, B B, C calls", and for a
// carrier how much of it was its virtual threads'.
func procThreadLine(t ProcThread) string {
	name := t.Name
	if name != "tid "+itoa(t.TID) {
		name += " (tid " + itoa(t.TID) + ")"
	}
	line := fmt.Sprintf("%s: %d events, %d B, %d calls", name, t.Events, t.Bytes, t.Calls)
	if t.Carried > 0 || t.CarriedCalls > 0 {
		line += fmt.Sprintf("; carrier: %d events and %d of the listed calls were virtual threads' I/O, not its own",
			t.Carried, t.CarriedCalls)
	}
	return line
}

// procFileLine is "PATH: R B read, W B written; OPS".
func procFileLine(f ProcFile) string {
	line := fmt.Sprintf("%s: %d B read, %d B written", f.Path, f.ReadBytes, f.WriteBytes)
	if len(f.Ops) > 0 {
		line += "; " + strings.Join(f.Ops, ", ")
	}
	if f.MoreOps > 0 {
		line += fmt.Sprintf(" (+%d more)", f.MoreOps)
	}
	return line
}

// socketLine is "ID ROLE LOCAL -> REMOTE, service S, peer P; bytes;
// requests".
func socketLine(s ProcSocket) string {
	peer := "peer untraced"
	if s.PeerState == report.PeerTraced && len(s.Peers) > 0 {
		peer = "peer " + strings.Join(s.Peers, ", ")
	}
	fd := "-"
	if s.FD >= 0 {
		fd = fmt.Sprint(s.FD)
	}
	return fmt.Sprintf("%s fd %s %s %s -> %s, service %s, %s; sent %d B, received %d B; %d requests", s.ID,
		fd, dashed(s.Role), dashed(s.Local), dashed(s.Remote), dashed(s.Service), peer, s.SentBytes, s.RecvBytes,
		s.Requests)
}

func moreLine(b *strings.Builder, what string, n int) {
	if n > 0 {
		fmt.Fprintf(b, "  truncated: %d more %s\n", n, what)
	}
}

func dashed(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
