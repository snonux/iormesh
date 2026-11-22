package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeMageEnv makes the test binary play the mage-generated main.
const fakeMageEnv = "IORMESH_TEST_FAKE_MAGE"

// fakeMageGrace stands in for mage's 5 seconds after SIGINT, and
// fakeTeardown for an rm that takes longer than that.
const (
	fakeMageGrace = 200 * time.Millisecond
	fakeTeardown  = 5 * fakeMageGrace
)

// TestInterruptOutlivesMageGrace sends SIGINT to a process that does what
// mage v1.17.2's generated main does around a target (template.go's
// runTarget: on SIGINT cancel the target's context, wait 5 seconds, then
// exit with "cleanup timeout exceeded") while the target, set up as
// `mage m4Acceptance` sets itself up, runs a teardown longer than that
// grace. The teardown must finish (task mi4).
func TestInterruptOutlivesMageGrace(t *testing.T) {
	if os.Getenv(fakeMageEnv) == "1" {
		fakeMage()
		return
	}
	out, err := runFakeMage(t, "TestInterruptOutlivesMageGrace", false)
	if !strings.Contains(out, "teardown done") {
		t.Fatalf("the teardown was cut off (exit %v):\n%s", err, out)
	}
	if err != nil {
		t.Fatalf("fake mage: %v\n%s", err, out)
	}
}

// TestSecondInterruptKills: a second SIGINT during the teardown ends the
// process at once with 130, neither the teardown nor mage's grace
// finishing, and says the cleanup is left to the operator.
func TestSecondInterruptKills(t *testing.T) {
	if os.Getenv(fakeMageEnv) == "1" {
		fakeMage()
		return
	}
	out, err := runFakeMage(t, "TestSecondInterruptKills", true)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 128+int(syscall.SIGINT) {
		t.Fatalf("want exit 130, got %v:\n%s", err, out)
	}
	if strings.Contains(out, "teardown done") || strings.Contains(out, "Error:") {
		t.Fatalf("the second SIGINT did not end the process at once:\n%s", out)
	}
	if !strings.Contains(out, "interrupt again: exiting, the teardown unfinished") {
		t.Fatalf("no word of the unfinished teardown:\n%s", out)
	}
}

// runFakeMage runs test in a child playing fakeMage, sends it SIGINT
// once the target runs (and again once its teardown started, if twice),
// and returns its output and exit.
func runFakeMage(t *testing.T, test string, twice bool) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$")
	cmd.Env = append(os.Environ(), fakeMageEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		out.WriteString(sc.Text() + "\n")
		if sc.Text() == "target running" || (twice && sc.Text() == "teardown started") {
			if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
				t.Fatal(err)
			}
		}
	}
	err = cmd.Wait()
	return out.String() + stderr.String(), err
}

// fakeMage is runTarget's signal handling, the grace shortened. Unlike
// mage it registers before starting the target, so the test does not
// depend on which goroutine runs first; the real target calls
// InterruptContext only after its builds, long after mage registered.
func fakeMage() {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	d := make(chan error, 1)
	go func() { d <- fakeTarget(ctx) }()
	select {
	case <-sigCh:
		cancel()
		select {
		case err := <-d:
			fakeMageExit(err)
		case <-time.After(fakeMageGrace):
			fakeMageExit(fmt.Errorf("cleanup timeout exceeded"))
		case <-sigCh:
			fakeMageExit(fmt.Errorf("exit forced"))
		}
	case err := <-d:
		fakeMageExit(err)
	}
}

func fakeMageExit(err error) {
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// fakeTarget is runM4Acceptance with a Runner whose teardown is slow.
func fakeTarget(ctx context.Context) error {
	ctx, stop := InterruptContext(ctx)
	defer stop()
	fmt.Println("target running")
	<-ctx.Done()
	cctx, cancel := cleanupCtx(ctx)
	defer cancel()
	fmt.Println("teardown started")
	t := time.NewTimer(fakeTeardown)
	defer t.Stop()
	select {
	case <-cctx.Done():
		return cctx.Err()
	case <-t.C:
	}
	fmt.Println("teardown done")
	return nil
}
