package viewmodel

import (
	"strconv"

	"iormesh/internal/report"
)

// Processes (V2, docs/visualization.md screen 4 "Process"): per host,
// each traced process incarnation with its threads, files and sockets.
// They are projected from the run's rollup (internal/report), which
// build already collects on the merge's read — no reading of the trace
// of their own, and the same counts `show report` prints. The model
// keeps a compact copy (what a screen lists; the rollup's per-request
// and per-method detail stays in report.json) so the three surfaces
// render it from the model alone, and adds what only the model knows:
// the name the process carries on the conversations' lifelines.
//
// A kernel thread that carried virtual threads keeps their I/O apart:
// Carried and CarriedCalls count it, so a surface never shows it as the
// carrier's own work (report.Thread.Carried).

// Process is one traced process incarnation.
type Process struct {
	// ID is the merged graph's process node id.
	ID      string `json:"id"`
	Project string `json:"project"`
	// Host is the store's host directory, FQDN the session's.
	Host        string `json:"host"`
	FQDN        string `json:"fqdn"`
	PID         uint32 `json:"pid"`
	Incarnation uint64 `json:"pid_incarnation"`
	// Incarnations counts this host's incarnations of the pid in the
	// list: more than one is a restart (or a reused pid).
	Incarnations int    `json:"incarnations"`
	Comm         string `json:"comm"`
	App          string `json:"app"`
	// Service is its name on the conversations' lifelines: the service
	// its app serves, else the app, else the comm (namer.process).
	Service     string       `json:"service"`
	Serves      []string     `json:"serves"`
	Events      uint64       `json:"events"`
	JavaEvents  uint64       `json:"java_events"`
	Threads     []ProcThread `json:"threads"`
	MoreThreads int          `json:"more_threads"`
	Files       []ProcFile   `json:"files"`
	MoreFiles   int          `json:"more_files"`
	Sockets     []ProcSocket `json:"sockets"`
	MoreSockets int          `json:"more_sockets"`
}

// ProcThread is one kernel thread of a process, in first-seen order.
type ProcThread struct {
	// Name is the Java thread name, else "tid N".
	Name   string `json:"name"`
	TID    uint32 `json:"tid"`
	Events uint64 `json:"events"`
	Bytes  uint64 `json:"bytes"`
	// Carried counts the events among Events that were virtual threads'
	// I/O it carried; CarriedCalls the requests among Calls.
	Carried      uint64 `json:"carried"`
	Calls        int    `json:"calls"`
	CarriedCalls int    `json:"carried_calls"`
}

// ProcFile is one path the process opened or changed.
type ProcFile struct {
	Path       string   `json:"path"`
	ReadBytes  uint64   `json:"read_bytes"`
	WriteBytes uint64   `json:"write_bytes"`
	Ops        []string `json:"ops"`
	MoreOps    int      `json:"more_ops"`
}

// ProcSocket is one connection end of the process.
type ProcSocket struct {
	// ID is the merged conn id (`iormeshctl query -conn`).
	ID string `json:"id"`
	// FD is the descriptor holding this end (-1 unknown): a process may
	// hold both ends of one connection or pipe (report.Conn.FD).
	FD        int32    `json:"fd"`
	Role      string   `json:"role"`
	Local     string   `json:"local"`
	Remote    string   `json:"remote"`
	Service   string   `json:"service"`
	PeerState string   `json:"peer_state"`
	Peers     []string `json:"peers"`
	SentBytes uint64   `json:"sent_bytes"`
	RecvBytes uint64   `json:"recv_bytes"`
	Requests  int      `json:"requests"`
}

// processes projects the rollup's processes, in its order (project,
// host, pid, incarnation).
func processes(r *report.Report, n namer) ([]Process, int) {
	out := []Process{}
	if r == nil {
		return out, 0
	}
	incs := map[[2]string]int{}
	for _, p := range r.Processes {
		incs[[2]string{p.Host, itoa(p.PID)}]++
	}
	for _, p := range r.Processes {
		out = append(out, Process{ID: p.ID, Project: p.Project, Host: p.Host, FQDN: p.FQDN, PID: p.PID,
			Incarnation: p.Incarnation, Incarnations: incs[[2]string{p.Host, itoa(p.PID)}], Comm: p.Comm,
			App: p.App, Service: n.processID(p.ID, p.PID), Serves: nonNil(p.Serves), Events: p.Events,
			JavaEvents: p.JavaEvents, Threads: procThreads(p.Threads), MoreThreads: p.MoreThreads,
			Files: procFiles(p.Files), MoreFiles: p.MoreFiles, Sockets: procSockets(p.Conns),
			MoreSockets: p.MoreConns})
	}
	return out, r.MoreProcesses
}

// procThreads projects a process's threads.
func procThreads(ts []report.Thread) []ProcThread {
	out := make([]ProcThread, 0, len(ts))
	for _, t := range ts {
		pt := ProcThread{Name: t.Name, TID: t.TID, Events: t.Events, Bytes: t.Bytes, Carried: t.Carried,
			Calls: len(t.Calls) + t.MoreCalls}
		for _, c := range t.Calls {
			pt.CarriedCalls += btoi(c.Carrier)
		}
		out = append(out, pt)
	}
	return out
}

// procFiles projects a process's files.
func procFiles(fs []report.File) []ProcFile {
	out := make([]ProcFile, 0, len(fs))
	for _, f := range fs {
		out = append(out, ProcFile{Path: f.Path, ReadBytes: f.ReadBytes, WriteBytes: f.WriteBytes,
			Ops: nonNil(f.Ops), MoreOps: f.MoreOps})
	}
	return out
}

// procSockets projects a process's connection ends.
func procSockets(cs []report.Conn) []ProcSocket {
	out := make([]ProcSocket, 0, len(cs))
	for _, c := range cs {
		out = append(out, ProcSocket{ID: c.ID, FD: c.FD, Role: c.Role, Local: c.Local, Remote: c.Remote,
			Service: c.Service, PeerState: c.PeerState, Peers: nonNil(c.Peers), SentBytes: c.SentBytes,
			RecvBytes: c.RecvBytes, Requests: len(c.Requests) + c.MoreRequests})
	}
	return out
}

// nonNil turns a nil slice into an empty one, so the JSON says [].
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }
