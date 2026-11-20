package internal_test

// Root test of PID-namespace-aware targeting (M7 step 1, decision #13, task
// 4f4; internal/c/pidns.c). The test binary re-runs itself as a tracer in a
// fresh PID namespace (unshare --pid --fork --mount-proc) - a pod's tracer
// in miniature - while a host process keeps opening probePath outside it.
// Listed in gatecmd.RootBPFTests; `mage bpfRootTest` runs it.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	ibpf "iormesh/internal/bpf"
	"iormesh/internal/types"
)

// pidnsChildEnv makes the test binary the namespace side (TestMain): its
// value is "HOST,SIBLING", the host pid of the bystander outside the
// namespace and the pid of the sibling namespace's loop as that namespace
// numbers it.
const pidnsChildEnv = "IORMESH_PIDNS_CHILD"

// siblingScript runs in a sibling PID namespace (its own, beside the
// tracer's): it burns 200 pids first, so the loop's number there is no
// small pid the tracer's namespace may also hold, prints that number, and
// opens probePath every 20 ms until killed.
var siblingScript = fmt.Sprintf(`for i in $(seq 200); do /bin/true; done
( while :; do exec 3< %s; exec 3<&-; sleep 0.02; done ) &
echo $!; wait`, probePath)

// TestPidNSTracerConfined: a tracer in its own PID namespace traces a
// workload there by the namespace's pids - its forked child too - and one in
// a namespace nested below; it never traces the host process whose host pid
// it was told to target, nor a process of a sibling namespace whose number
// there it was told to target (what the namespace-inode check in pidns.c
// guards). The host side keeps both busy; the namespace side
// (runPidNSChild) checks the records and fails the run.
func TestPidNSTracerConfined(t *testing.T) {
	requireRoot(t)
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("needs unshare(1)")
	}
	bystander := startTarget(t)
	sibling := startSibling(t)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				if _, err := io.WriteString(bystander.stdin, "go\n"); err != nil || !bystander.acks.Scan() {
					return
				}
			}
		}
	}()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("unshare", "--pid", "--fork", "--mount-proc", self)
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d,%d", pidnsChildEnv, bystander.pid(), sibling))
	out, err := cmd.CombinedOutput()
	close(stop)
	<-done
	if err != nil {
		t.Fatalf("namespace tracer: %v\n%s", err, out)
	}
	t.Logf("namespace tracer: %s", bytes.TrimSpace(out))
}

// startSibling starts siblingScript in a PID namespace of its own and
// returns its loop's pid as that namespace numbers it.
func startSibling(t *testing.T) uint32 {
	t.Helper()
	tg := startScript(t, "unshare", "--pid", "--fork", "--kill-child", "bash", "-c", siblingScript)
	if !tg.acks.Scan() {
		t.Fatalf("sibling namespace: no pid: %v", tg.acks.Err())
	}
	pid, err := strconv.ParseUint(tg.acks.Text(), 10, 32)
	if err != nil {
		t.Fatalf("sibling namespace: %q is no pid: %v", tg.acks.Text(), err)
	}
	t.Cleanup(func() { _ = tg.cmd.Process.Kill() })
	return uint32(pid)
}

// nsWorkload is a forkingScript bash the namespace side started.
type nsWorkload struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	acks  *bufio.Scanner
}

func startNSWorkload(argv ...string) (*nsWorkload, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &nsWorkload{cmd: cmd, stdin: stdin, acks: bufio.NewScanner(stdout)}, nil
}

func (w *nsWorkload) stop() {
	_ = w.stdin.Close()
	_ = w.cmd.Wait()
}

// poke makes the workload fork its child once and returns the ack: the
// child's pid in the workload's own namespace.
func (w *nsWorkload) poke() (string, error) {
	if _, err := io.WriteString(w.stdin, "go\n"); err != nil {
		return "", err
	}
	if !w.acks.Scan() {
		return "", fmt.Errorf("workload %d did not acknowledge: %v", w.cmd.Process.Pid, w.acks.Err())
	}
	return w.acks.Text(), nil
}

// childPid waits for pid's first child (the bash a nested unshare forks)
// and returns it as the tracer's namespace numbers it.
func childPid(pid int) (uint32, error) {
	path := fmt.Sprintf("/proc/%d/task/%d/children", pid, pid)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		if f := strings.Fields(string(raw)); len(f) > 0 {
			n, err := strconv.ParseUint(f[0], 10, 32)
			return uint32(n), err
		}
	}
	return 0, fmt.Errorf("no child of %d", pid)
}

// nsTrace is what the namespace side saw: probePath opens by pid, the new
// processes task_newtask reported per creator, every record's pid, and how
// many records carried pid 0 (which no task-context record may).
type nsTrace struct {
	opens  map[uint32]int
	forked map[uint32][]uint32
	pids   map[uint32]bool
	zero   *int
}

func collectNSTrace(tr *ibpf.Tracer, wait time.Duration) nsTrace {
	got := nsTrace{opens: map[uint32]int{}, forked: map[uint32][]uint32{}, pids: map[uint32]bool{}, zero: new(int)}
	deadline := time.After(wait)
	for {
		select {
		case batch, ok := <-tr.Records():
			if !ok {
				return got
			}
			for _, rec := range batch {
				got.add(rec)
			}
			tr.Release(batch)
		case <-deadline:
			return got
		}
	}
}

func (n nsTrace) add(rec any) {
	if pid, ok := recordPid(rec); ok {
		n.pids[pid] = true
		if pid == 0 {
			*n.zero++
		}
	}
	switch ev := rec.(type) {
	case *types.OpenEvent:
		if strings.HasSuffix(types.StringValue(ev.Filename[:]), probePath) {
			n.opens[ev.Pid]++
		}
	case *types.TaskNewtaskEvent:
		if ev.Pid != ev.CreatorPid {
			n.forked[ev.CreatorPid] = append(n.forked[ev.CreatorPid], ev.Pid)
		}
	}
}

// recordPid is a record's Pid field: every kernel record with a task
// context has one (the generated types are plain structs). The TCP state
// record has none of its own - its pid is the socket owner's - and is skipped.
func recordPid(rec any) (uint32, bool) {
	if _, ok := rec.(*types.TcpStateEvent); ok {
		return 0, false
	}
	v := reflect.ValueOf(rec)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return 0, false
	}
	f := v.Elem().FieldByName("Pid")
	if !f.IsValid() || f.Kind() != reflect.Uint32 {
		return 0, false
	}
	return uint32(f.Uint()), true
}

// pidnsChildMain runs the namespace side and turns its verdict into an
// exit status.
func pidnsChildMain(env string) int {
	var host, sibling uint32
	_, err := fmt.Sscanf(env, "%d,%d", &host, &sibling)
	if err == nil {
		err = runPidNSChild(host, sibling)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pidns child:", err)
		return 1
	}
	return 0
}

// runPidNSChild is the namespace side of TestPidNSTracerConfined.
func runPidNSChild(hostPid, siblingPid uint32) error {
	tr, err := ibpf.Open(ibpf.Config{})
	if err != nil {
		return fmt.Errorf("open tracer: %w", err)
	}
	defer func() { _ = tr.Close() }()
	if tr.PidNS() == 0 {
		return errors.New("the tracer reports the initial PID namespace")
	}
	same, err := startNSWorkload("bash", "-c", forkingScript)
	if err != nil {
		return err
	}
	defer same.stop()
	nested, err := startNSWorkload("unshare", "--pid", "--fork", "--mount-proc", "bash", "-c", forkingScript)
	if err != nil {
		return err
	}
	defer nested.stop()
	nestedBash, err := childPid(nested.cmd.Process.Pid)
	if err != nil {
		return err
	}
	samePid := uint32(same.cmd.Process.Pid)
	if samePid == siblingPid || nestedBash == siblingPid {
		return fmt.Errorf("sibling pid %d is also a workload's here: the check would prove nothing", siblingPid)
	}
	for _, pid := range []uint32{samePid, nestedBash, hostPid, siblingPid} {
		if err := tr.AddTarget(pid); err != nil {
			return fmt.Errorf("add target %d: %w", pid, err)
		}
	}
	return checkPidNSTrace(tr, same, nested, nestedBash, [2]uint32{hostPid, siblingPid})
}

// checkPidNSTrace pokes both workloads and holds the records to the
// namespace's pids.
func checkPidNSTrace(tr *ibpf.Tracer, same, nested *nsWorkload, nestedBash uint32, outside [2]uint32) error {
	ack, err := same.poke()
	if err != nil {
		return err
	}
	sameChild, err := strconv.ParseUint(ack, 10, 32)
	if err != nil {
		return fmt.Errorf("ack %q is no pid: %w", ack, err)
	}
	if _, err := nested.poke(); err != nil {
		return err
	}
	got := collectNSTrace(tr, 2*time.Second)
	var errs []error
	if got.opens[uint32(sameChild)] == 0 {
		errs = append(errs, fmt.Errorf("same-namespace child %d: no open of %s (opens %v)", sameChild, probePath, got.opens))
	}
	kids := got.forked[nestedBash]
	if !slices.ContainsFunc(kids, func(p uint32) bool { return got.opens[p] > 0 }) {
		errs = append(errs, fmt.Errorf("nested bash %d: no traced child (forked %v, opens %v)", nestedBash, kids, got.opens))
	}
	for _, pid := range outside {
		if got.pids[pid] {
			errs = append(errs, fmt.Errorf("pid %d of a process outside the namespace was traced", pid))
		}
	}
	for pid := range got.opens {
		if pid != uint32(sameChild) && !slices.Contains(kids, pid) {
			errs = append(errs, fmt.Errorf("pid %d opened %s, but is no workload's child (opens %v)", pid, probePath, got.opens))
		}
	}
	if *got.zero != 0 {
		errs = append(errs, fmt.Errorf("%d records carry pid 0", *got.zero))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	fmt.Printf("ok: pidns %d, same-namespace child %d, nested bash %d forked %v\n",
		tr.PidNS(), sameChild, nestedBash, kids)
	return nil
}
