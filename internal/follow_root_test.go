package internal_test

// Root tests of M2 steps 1–2 on the real BPF object: process-tree
// following (a traced process's forked and exec'd child is traced from its
// first syscall, an untraced one's is not, and an exited process leaves
// target_map) and the pattern session's exec watch (an untraced exec is
// reported with its argv, adopted with a counted attach gap, or dismissed).
// Listed in gatecmd.RootBPFTests; `mage bpfRootTest` runs them.

import (
	"fmt"
	"os/exec"
	"slices"
	"testing"
	"time"

	ibpf "iormesh/internal/bpf"
	tspec "iormesh/internal/target"
	"iormesh/internal/types"
)

// forkingScript makes bash fork a child that execs bash, which opens,
// reads and closes probePath in its own PID, once per poke. It prints the
// child's PID ($!) as the acknowledgement.
var forkingScript = fmt.Sprintf(
	`while read -r _; do bash -c 'exec 3< %s; read -r -u 3 _; exec 3<&-' & wait $!; echo $!; done`, probePath)

// childOpens collects, for wait, the open paths of every pid other than
// parent, and the pids task_newtask reported as new processes of parent.
func childOpens(tr *ibpf.Tracer, parent uint32, wait time.Duration) (opens map[uint32][]string, forked []uint32) {
	opens = map[uint32][]string{}
	deadline := time.After(wait)
	for {
		select {
		case batch, ok := <-tr.Records():
			if !ok {
				return opens, forked
			}
			for _, rec := range batch {
				switch ev := rec.(type) {
				case *types.OpenEvent:
					if ev.Pid != parent {
						opens[ev.Pid] = append(opens[ev.Pid], types.StringValue(ev.Filename[:]))
					}
				case *types.TaskNewtaskEvent:
					if ev.CreatorPid == parent && ev.Pid != parent {
						forked = append(forked, ev.Pid)
					}
				}
			}
		case <-deadline:
			return opens, forked
		}
	}
}

// pokeChild pokes a forkingScript target and returns the child's PID.
func pokeChild(t *testing.T, tg *target) uint32 {
	t.Helper()
	if _, err := tg.stdin.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	if !tg.acks.Scan() {
		t.Fatalf("target %d did not acknowledge: %v", tg.pid(), tg.acks.Err())
	}
	var child uint32
	if _, err := fmt.Sscan(tg.acks.Text(), &child); err != nil {
		t.Fatalf("ack %q is no PID: %v", tg.acks.Text(), err)
	}
	return child
}

// TestFollowForkExec: the child a traced bash forks — which then execs
// bash — is traced from the start: its task_newtask record names it, its
// open of probePath arrives under its own PID, and once it exited the
// kernel has dropped it from target_map again.
func TestFollowForkExec(t *testing.T) {
	tg := startScript(t, "bash", "-c", forkingScript)
	tr := openTracer(t, tg.pid())
	child := pokeChild(t, tg)
	opens, forked := childOpens(tr, tg.pid(), 2*time.Second)
	if !slices.Contains(forked, child) {
		t.Fatalf("no task_newtask record for child %d (forked: %v)", child, forked)
	}
	if !contains(opens[child], probePath) {
		t.Fatalf("child %d's open of %s not traced; opens by pid: %v", child, probePath, opens)
	}
	traced, err := tr.Targeted(child)
	if err != nil || traced {
		t.Fatalf("exited child %d still in target_map (%v, %v)", child, traced, err)
	}
}

// TestUntracedForkNotFollowed: the same fork from an untraced bash yields
// nothing — then, as the positive control, it is followed once the
// parent is a target.
func TestUntracedForkNotFollowed(t *testing.T) {
	tg := startScript(t, "bash", "-c", forkingScript)
	tr := openTracer(t)
	child := pokeChild(t, tg)
	if opens, forked := childOpens(tr, tg.pid(), 500*time.Millisecond); len(opens) != 0 || len(forked) != 0 {
		t.Fatalf("untraced parent's child %d traced: opens %v forked %v", child, opens, forked)
	}
	if err := tr.AddTarget(tg.pid()); err != nil {
		t.Fatal(err)
	}
	child = pokeChild(t, tg)
	if opens, _ := childOpens(tr, tg.pid(), 2*time.Second); !contains(opens[child], probePath) {
		t.Fatalf("after AddTarget: child %d not followed; opens %v", child, opens)
	}
}

// watchScript is a sh whose $0 carries marker, so its argv is unique, and
// which opens probePath once per poke.
func watchScript(marker string) []string {
	script := fmt.Sprintf(`while read -r _; do exec 3< %s; read -r -u 3 _; exec 3<&-; echo ok; done`, probePath)
	return []string{"-c", script, marker}
}

// execOf waits for the exec record of pid.
func execOf(t *testing.T, tr *ibpf.Tracer, pid uint32) *types.ExecArgvEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case batch := <-tr.Records():
			for _, rec := range batch {
				if ev, ok := rec.(*types.ExecArgvEvent); ok && ev.Pid == pid {
					return ev
				}
			}
		case <-deadline:
			t.Fatalf("no exec record for pid %d", pid)
		}
	}
}

// TestExecWatchAdoptAndIgnore: with the exec watch on and no target, a
// new process's exec is reported with its argv (the marker matches the
// pattern, the comm "sh" never could) and exe. Adopted after its start-up
// syscalls, it reports a non-zero attach gap and is traced; a second one,
// dismissed, stays untraced.
func TestExecWatchAdoptAndIgnore(t *testing.T) {
	tr := openTracerWith(t, ibpf.Config{ExecWatch: true})
	marker := fmt.Sprintf("iormesh-watch-%d", time.Now().UnixNano())
	spec := tspec.Spec{Pattern: "*" + marker + "*"}
	adopted := startScript(t, "sh", watchScript(marker)...)
	ev := execOf(t, tr, adopted.pid())
	argv := tspec.SplitArgv(ev.Argv[:ev.ArgvLen])
	if ev.ArgvLen != ev.ArgvTotal || !spec.Matches(types.StringValue(ev.Exe[:]), argv) {
		t.Fatalf("exec record argv %q (len %d of %d) exe %q does not match %s",
			argv, ev.ArgvLen, ev.ArgvTotal, types.StringValue(ev.Exe[:]), spec.Pattern)
	}
	time.Sleep(200 * time.Millisecond) // sh has run its start-up and blocks in read
	gap, ok, err := tr.AdoptExec(ev.Pid)
	if err != nil || !ok || gap == 0 {
		t.Fatalf("AdoptExec = %d, %v, %v: want adopted with the start-up syscalls counted as the gap",
			gap, ok, err)
	}
	ignored := startScript(t, "sh", watchScript(marker+"-other")...)
	other := execOf(t, tr, ignored.pid())
	if err := tr.IgnoreExec(other.Pid, other.Time); err != nil {
		t.Fatal(err)
	}
	ignored.poke(t)
	adopted.poke(t)
	opens, _ := childOpens(tr, 0, 2*time.Second)
	if !contains(opens[adopted.pid()], probePath) {
		t.Fatalf("adopted pid %d not traced: %v", adopted.pid(), opens)
	}
	if len(opens[ignored.pid()]) != 0 {
		t.Fatalf("dismissed pid %d traced: %v", ignored.pid(), opens[ignored.pid()])
	}
	if traced, err := tr.Targeted(ignored.pid()); err != nil || traced {
		t.Fatalf("dismissed pid %d targeted (%v, %v)", ignored.pid(), traced, err)
	}
}

// TestExecWatchExitedBeforeAdopt: a matching process that exits before
// userspace's verdict is not adopted, and leaves no target_map entry —
// the kernel dropped its pending entry at the exit, and an insert now
// would never be removed and would trace whatever process reuses the PID.
// The same process, exec'ed again and still running, is the positive
// control: it is adopted.
func TestExecWatchExitedBeforeAdopt(t *testing.T) {
	tr := openTracerWith(t, ibpf.Config{ExecWatch: true})
	marker := fmt.Sprintf("iormesh-gone-%d", time.Now().UnixNano())
	cmd := exec.Command("sh", "-c", "exit 0", marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := uint32(cmd.Process.Pid)
	ev := execOf(t, tr, pid)
	if err := cmd.Wait(); err != nil { // exited and reaped before the verdict
		t.Fatal(err)
	}
	gap, adopted, err := tr.AdoptExec(ev.Pid)
	if err != nil || adopted {
		t.Fatalf("AdoptExec of exited pid %d = %d, %v, %v: want not adopted", pid, gap, adopted, err)
	}
	if traced, err := tr.Targeted(pid); err != nil || traced {
		t.Fatalf("exited pid %d left in target_map (%v, %v)", pid, traced, err)
	}
	live := startScript(t, "sh", watchScript(marker)...)
	if _, adopted, err := tr.AdoptExec(execOf(t, tr, live.pid()).Pid); err != nil || !adopted {
		t.Fatalf("control: running pid %d not adopted (%v, %v)", live.pid(), adopted, err)
	}
}
