package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"iormesh/internal/federate"
)

// hostGate hands out a Transport for the two named hosts and refuses
// every other name, so no code path of the run can reach a third
// machine, whatever a build output or a remote answer says.
type hostGate struct {
	allowed map[string]bool
	dial    func(host string) (federate.Transport, error)
	mu      sync.Mutex
	open    map[string]federate.Transport
}

func newHostGate(h1, h2 string, dial func(string) (federate.Transport, error)) *hostGate {
	return &hostGate{allowed: map[string]bool{h1: true, h2: true}, dial: dial,
		open: map[string]federate.Transport{}}
}

// ErrForeignHost is a request for a host other than HOST1 and HOST2.
var ErrForeignHost = errors.New("refusing a host other than HOST1 and HOST2")

func (g *hostGate) get(host string) (federate.Transport, error) {
	if !g.allowed[host] {
		return nil, fmt.Errorf("%w: %q", ErrForeignHost, host)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if t, ok := g.open[host]; ok {
		return t, nil
	}
	t, err := g.dial(host)
	if err != nil {
		return nil, err
	}
	g.open[host] = t
	return t, nil
}

// remote runs argv on host and returns its trimmed stdout.
func (r *Runner) remote(ctx context.Context, host string, argv ...string) (string, error) {
	t, err := r.hosts.get(host)
	if err != nil {
		return "", err
	}
	r.logf("  %s$ %s", host, strings.Join(argv, " "))
	out, err := t.Run(ctx, argv...)
	return strings.TrimSpace(string(out)), err
}

// remoteTest runs a `test`-like command: true on exit 0, false on exit 1,
// an error otherwise (ssh's 255 is a failure, never a "no").
func (r *Runner) remoteTest(ctx context.Context, host string, argv ...string) (bool, error) {
	_, err := r.remote(ctx, host, argv...)
	switch code := exitCode(err); {
	case err == nil:
		return true, nil
	case code == 1:
		return false, nil
	default:
		return false, err
	}
}

// exitCode is the remote command's exit status, -1 when it did not run.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// lines splits command output into its non-empty lines.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// waitListening polls until something listens on host's TCP port, so a
// client is released only after its server is ready (the harness waits
// for the server's `listening` line the same way).
func (r *Runner) waitListening(ctx context.Context, host string, port int) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out, err := r.remote(ctx, host, "ss", "-Htln", fmt.Sprintf("sport = :%d", port))
		if err != nil {
			return err
		}
		if out != "" {
			return nil
		}
		if err := r.sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return fmt.Errorf("%s: nothing listens on port %d after 60s", host, port)
}
