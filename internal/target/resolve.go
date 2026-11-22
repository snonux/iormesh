package target

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// tcpListen is the st column of a LISTEN socket in /proc/net/tcp{,6}.
const tcpListen = "0A"

// Resolve returns the tgids a valid spec names right now, sorted:
//   - PIDs: the PIDs themselves, each required to be a live process;
//   - Pattern: every process whose command line or executable matches,
//     possibly none — later ones are matched on exec. self and its
//     ancestors are skipped (ProcFS.Ancestors);
//   - ListenPort: the owners of the sockets listening on the port, at
//     least one, never a service manager (ListenOwners).
func (s Spec) Resolve(p ProcFS, self uint32) ([]uint32, error) {
	switch s.Mode() {
	case ModePattern:
		return s.resolvePattern(p, self)
	case ModeListenPort:
		return p.ListenOwners(s.ListenPort)
	}
	for _, pid := range s.PIDs {
		if !p.Alive(pid, 0) {
			return nil, fmt.Errorf("%w: pid %d: no such process", ErrSpec, pid)
		}
	}
	return slices.Sorted(slices.Values(s.PIDs)), nil
}

// resolvePattern scans the tree for running matches. A process that
// vanishes during the scan is simply not a match. Skipped: self and its
// ancestors, and every process running self's executable — another
// iormesh session's command line names its pattern too (IsTracer).
func (s Spec) resolvePattern(p ProcFS, self uint32) ([]uint32, error) {
	m, err := s.Matcher()
	if err != nil {
		return nil, err
	}
	pids, err := p.PIDs()
	if err != nil {
		return nil, err
	}
	skip, selfExe := p.Ancestors(self), p.Exe(self)
	var out []uint32
	for _, pid := range pids {
		if skip[pid] {
			continue
		}
		argv, err := p.Cmdline(pid)
		if err != nil {
			continue
		}
		exe := p.Exe(pid)
		if m.Match(exe, argv) && !IsTracer(exe, selfExe) {
			out = append(out, pid)
		}
	}
	slices.Sort(out)
	return out, nil
}

// IsTracer reports whether exe, a process's executable, is the tracer's
// own (selfExe): an iormesh session is never a target, though its
// command line — "iormesh run -pattern X" — matches X. Both empty or
// unknown is no evidence.
func IsTracer(exe, selfExe string) bool {
	return exe != "" && exe == selfExe
}

// ListenOwners returns the processes holding a TCP socket (IPv4 or IPv6)
// that listens on port, sorted. None is an error: the session would trace
// nothing. Sockets in other network namespaces are not seen.
//
// A service manager holding the listener (systemd socket activation:
// pid 1 or a user manager keeps the socket and hands it to the service)
// is never an owner (decision #23, task mh4): tracing it would follow
// every service it spawns. A port only a manager holds is an error of
// its own — the service is not running.
func (p ProcFS) ListenOwners(port uint16) ([]uint32, error) {
	hs, err := p.ListenHolders(port)
	if err != nil {
		return nil, err
	}
	pids := make([]uint32, len(hs))
	for i, h := range hs {
		pids[i] = h.PID
	}
	return pids, nil
}

// Holder is a process holding a listening socket, as one listing saw it:
// its pid and the start time read before the last read of its fd table.
// A port session adopts the holder only while the pid still has that
// start time (decision #23, task qj4): a pid reused after the listing by
// a process that does not hold the port is not the holder.
type Holder struct {
	PID     uint32
	StartNS uint64
}

// ListenHolders is ListenOwners with each owner's start time (Holder),
// the same rules and errors.
func (p ProcFS) ListenHolders(port uint16) ([]Holder, error) {
	inodes := map[string]bool{}
	for _, f := range []string{"tcp", "tcp6"} {
		if err := listenInodes(filepath.Join(p.Root, "net", f), port, inodes); err != nil {
			return nil, err
		}
	}
	if len(inodes) == 0 {
		return nil, fmt.Errorf("%w: nothing listens on TCP port %d", ErrSpec, port)
	}
	owners, err := p.socketOwners(inodes)
	if err != nil {
		return nil, err
	}
	if len(owners) == 0 {
		return nil, fmt.Errorf("%w: no visible process owns the listener on TCP port %d", ErrSpec, port)
	}
	var apps []Holder
	var managers []uint32
	for _, h := range owners {
		if IsServiceManager(p.Exe(h.PID)) {
			managers = append(managers, h.PID)
		} else {
			apps = append(apps, h)
		}
	}
	if len(apps) == 0 {
		return nil, fmt.Errorf("%w: only the service manager (pid %v) holds the listener on TCP port %d "+
			"(socket activation; the service is not running)", ErrSpec, managers, port)
	}
	return apps, nil
}

// IsServiceManager reports whether exe, a process's executable, is a
// service manager that holds socket-activated listeners for its services:
// systemd, as pid 1 or as a user manager. Matched by name, since its path
// differs between distributions (/usr/lib/systemd/systemd on el9).
func IsServiceManager(exe string) bool {
	return exe != "" && filepath.Base(strings.TrimSuffix(exe, deletedSuffix)) == "systemd"
}

// listenInodes adds the inode of every LISTEN socket on port in one
// /proc/net/tcp-format table. A missing table (no IPv6) adds nothing.
func listenInodes(table string, port uint16, inodes map[string]bool) error {
	b, err := os.ReadFile(table)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	want := fmt.Sprintf(":%04X", port)
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Scan() // header
	for sc.Scan() {
		// sl local rem st tx:rx tr:when retr uid timeout inode
		f := strings.Fields(sc.Text())
		if len(f) >= 10 && strings.HasSuffix(f[1], want) && f[3] == tcpListen {
			inodes[f[9]] = true
		}
	}
	return sc.Err()
}

// socketOwners returns the processes with an fd on one of the socket
// inodes. Processes that vanish, hide their fds or have no readable start
// time are skipped. Only a pid whose fds hold the socket has its start
// time read (a resolve runs every second while a port session waits),
// and the fd table is read again after it: a holder confirmed after its
// start time was read is that incarnation, or — reused in between — a
// new process with the old start time, a mismatch adoption refuses
// (Holder) rather than a stranger passed off as the holder.
func (p ProcFS) socketOwners(inodes map[string]bool) ([]Holder, error) {
	pids, err := p.PIDs()
	if err != nil {
		return nil, err
	}
	var owners []Holder
	for _, pid := range pids {
		if !p.holdsSocket(pid, inodes) {
			continue
		}
		st, err := p.Stat(pid)
		if err != nil || st.StartNS == 0 || !p.holdsSocket(pid, inodes) {
			continue
		}
		owners = append(owners, Holder{PID: pid, StartNS: st.StartNS})
	}
	return owners, nil
}

// holdsSocket reports whether pid has an fd on one of the socket inodes.
func (p ProcFS) holdsSocket(pid uint32, inodes map[string]bool) bool {
	dir := filepath.Join(p.Root, strconv.FormatUint(uint64(pid), 10), "fd")
	fds, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		link, err := os.Readlink(filepath.Join(dir, fd.Name()))
		ino, ok := strings.CutPrefix(link, "socket:[")
		if err == nil && ok && inodes[strings.TrimSuffix(ino, "]")] {
			return true
		}
	}
	return false
}
