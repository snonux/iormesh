package schema

// fcntlSetFD is fcntl's F_SETFD command: the one fcntl whose argument a
// fcntl event carries (args.flags), since it decides which fds an exec
// closes.
const fcntlSetFD = 2

// validateLifecycleArgs keeps the args of task ze4 to their kinds: a
// pattern names the app of an attach, a parent the forker of a fork's
// child, and args.flags on a fcntl is F_SETFD's argument. Each one
// elsewhere would put an app, a forked edge or a close-on-exec change
// into a rebuilt graph (decision #3) that the tracer never saw. Both
// kinds name a process, so pid 0 (host-level) is no fork or attach.
func (e *Event) validateLifecycleArgs() error {
	a := e.Args
	switch {
	case a.Pattern != nil && e.Event != KindAttach:
		return invalidf("args.pattern on a %s event (attach only)", e.Event)
	case a.Pattern != nil && *a.Pattern == "":
		return invalidf("attach with an empty args.pattern")
	case (a.ParentPID != nil || a.ParentInc != nil) && e.Event != KindFork:
		return invalidf("args.parent_pid or args.parent_incarnation on a %s event (fork only)", e.Event)
	case (a.ParentPID == nil) != (a.ParentInc == nil):
		return invalidf("fork with only one of args.parent_pid and args.parent_incarnation")
	case (e.Event == KindFork || e.Event == KindAttach) && e.PID == 0:
		return invalidf("%s without a pid", e.Event)
	case e.Event == KindFcntl && a.Flags != nil && (a.Cmd == nil || *a.Cmd != fcntlSetFD):
		return invalidf("args.flags on a fcntl that is no F_SETFD")
	}
	return nil
}
