package target

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// Activation is a listener only a service manager held when a port
// session started: an idle socket-activated service (decision #23, task
// lk4). With no original owner there is no owner key to recognise the
// service by, so the socket itself is pinned: on activation the manager
// hands the very listening socket it holds to the service it starts.
type Activation struct {
	Port uint16
	// Inodes are the listening sockets the manager held.
	Inodes map[string]bool
	// Roots are the cgroup subtrees the holding managers run their units
	// in (managerRoot).
	Roots []Root
}

// Root is a cgroup subtree a service manager runs its units in: "/" for
// pid 1, the user@UID.service of a user manager (the parent of its
// init.scope). A user manager's services run under its own uid (task
// el4): a process in its tree under another uid is not one of them.
type Root struct {
	Path string
	// User marks a user manager's subtree; UID is that manager's
	// effective uid, which its services must have too.
	User bool
	UID  uint32
}

// ManagerListener pins the listener on port for a port session that
// waits for its socket-activated service (ActivatedHolders). The port
// must be held by service managers alone, each with a known unit
// subtree (managerRoot). A user manager whose subtree is not known is
// skipped — its services could not be told from any other unit's — and
// if none is left, nothing is pinned.
func (p ProcFS) ManagerListener(port uint16) (Activation, error) {
	inodes, err := p.listenSockets(port)
	if err != nil {
		return Activation{}, err
	}
	owners, err := p.socketOwners(inodes)
	if err != nil {
		return Activation{}, err
	}
	a := Activation{Port: port, Inodes: map[string]bool{}}
	for _, h := range owners {
		if !IsServiceManager(p.Exe(h.PID)) {
			return Activation{}, fmt.Errorf("%w: pid %d holds the listener on TCP port %d: not socket activation",
				ErrSpec, h.PID, port)
		}
		root, ok := p.managerRoot(h.PID)
		if !ok {
			continue
		}
		if !slices.Contains(a.Roots, root) {
			a.Roots = append(a.Roots, root)
		}
		for ino := range inodes {
			if p.holdsSocket(h.PID, map[string]bool{ino: true}) {
				a.Inodes[ino] = true
			}
		}
	}
	if len(a.Roots) == 0 || len(a.Inodes) == 0 {
		return Activation{}, fmt.Errorf("%w: no service manager with a known cgroup holds the listener on TCP port %d",
			ErrSpec, port)
	}
	return a, nil
}

// managerRoot returns the unit subtree of the service manager pid: the
// whole tree for pid 1. A user manager runs in the init.scope of its own
// unit, user@UID.service with UID its effective uid, and that unit is
// its subtree (task el4: strictly so). A manager anywhere else — a
// systemd run inside some service, an init.scope below another unit or
// another user's — or with no readable cgroup or uid has none known.
func (p ProcFS) managerRoot(pid uint32) (Root, bool) {
	if pid == 1 {
		return Root{Path: "/"}, true
	}
	uid, err := p.statusField(pid, "Uid:", 1)
	if err != nil {
		return Root{}, false
	}
	cg := p.procCgroup(pid)
	unit := path.Dir(cg)
	if path.Base(cg) != "init.scope" || path.Base(unit) != fmt.Sprintf("user@%d.service", uid) {
		return Root{}, false
	}
	return Root{Path: unit, User: true, UID: uid}, true
}

// ActivatedHolders lists the holders of a's pinned sockets that still
// listen on its port, split into the activated service (adopt) and the
// rest (refuse); service managers are in neither. A holder is the
// service only if it runs in a .service unit inside a manager's subtree
// — for a user manager, under its uid (serviceCgroup): a login-session
// process or another unit's process with a copy of the fd is not.
// Nothing listening on the port is no error, only nothing to adopt yet;
// an unreadable socket table is.
func (p ProcFS) ActivatedHolders(a Activation) (adopt, refuse []Holder, err error) {
	inodes, err := p.listenSockets(a.Port)
	if errors.Is(err, ErrSpec) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for ino := range inodes {
		if !a.Inodes[ino] {
			delete(inodes, ino) // not the manager's socket: another listener
		}
	}
	if len(inodes) == 0 {
		return nil, nil, nil
	}
	owners, err := p.socketOwners(inodes)
	if err != nil {
		return nil, nil, err
	}
	for _, h := range owners {
		if IsServiceManager(p.Exe(h.PID)) {
			continue
		}
		uid, uerr := p.statusField(h.PID, "Uid:", 1)
		if a.serviceCgroup(p.procCgroup(h.PID), uid, uerr == nil) {
			adopt = append(adopt, h)
		} else {
			refuse = append(refuse, h)
		}
	}
	return adopt, refuse, nil
}

// serviceCgroup reports whether cg, a holder's cgroup, is a service unit
// one of the managers runs: below a root, the first path element that
// is not a slice names a .service (anything under it is the unit's own
// delegated sub-cgroup). Below a user manager's root the holder must
// also have that manager's effective uid (uid, read when uidOK; task
// el4): a setuid program or one with an unreadable status is not its
// service. A scope (a login session, the manager's own init.scope) or an
// unknown cgroup is not, and neither is a user manager's own unit
// (user@UID.service) seen from pid 1's root: what runs in it is that
// manager's, judged only against its own root. pid 1's services may run
// under any uid (User=).
func (a Activation) serviceCgroup(cg string, uid uint32, uidOK bool) bool {
	if cg == "" {
		return false
	}
	for _, root := range a.Roots {
		if root.User && (!uidOK || uid != root.UID) {
			continue
		}
		rest, ok := strings.CutPrefix(cg, strings.TrimSuffix(root.Path, "/")+"/")
		if ok && unitIsService(rest) {
			return true
		}
	}
	return false
}

// unitIsService reports whether rel, a cgroup path below a manager's
// root, lies in a .service unit other than a user manager's: slices
// skipped, the first other element decides.
func unitIsService(rel string) bool {
	for elem := range strings.SplitSeq(rel, "/") {
		if !strings.HasSuffix(elem, ".slice") {
			return strings.HasSuffix(elem, ".service") && !strings.HasPrefix(elem, "user@")
		}
	}
	return false
}
