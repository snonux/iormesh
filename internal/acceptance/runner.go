package acceptance

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"iormesh/internal/federate"
)

// Deps are the Runner's connections to the world, injected so the unit
// tests drive the whole sequence against fakes without any host.
type Deps struct {
	Ctl Ctl
	// Dial opens the Transport to a host; the Runner only ever asks for
	// HOST1 or HOST2 (hostGate).
	Dial func(host string) (federate.Transport, error)
	Out  io.Writer
	// Sleep waits d or until ctx ends; nil is the real clock.
	Sleep func(ctx context.Context, d time.Duration) error
}

// hostInfo is what the preflight learned about a server.
type hostInfo struct {
	home, arch, kernel, machineID, addr string
}

// Runner runs the acceptance sequence once.
type Runner struct {
	cfg   Config
	ctl   Ctl
	hosts *hostGate
	out   io.Writer
	sleep func(ctx context.Context, d time.Duration) error
	res   results
	info  map[string]*hostInfo
	// workloadErr is a scenario run's failure, keyed by criterion, so the
	// check of that criterion reports it instead of a confusing graph.
	workloadErr map[string]error
	// builds keeps output bytes for the offline rebuild comparison.
	crossBytes map[string][]byte
	// added are the hosts with a registry entry, the ones rm must remove;
	// diskAdded says the watchdog project exists.
	added     map[string]bool
	diskAdded bool
}

// New returns a Runner for cfg.
func New(cfg Config, d Deps) *Runner {
	sleep := d.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	return &Runner{cfg: cfg, ctl: d.Ctl, hosts: newHostGate(cfg.Host1, cfg.Host2, d.Dial),
		out: d.Out, sleep: sleep, res: results{}, info: map[string]*hostInfo{},
		workloadErr: map[string]error{}, added: map[string]bool{}}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// microHostGap is why M4-9 cannot run yet; it is reported, never hidden.
const microHostGap = "workload gap: microservices binds 127.0.0.1 and pins certificates " +
	"naming 127.0.0.1 only (docs/implementation.md, \"The microservices scenario\"); " +
	"spreading it over hosts needs --bind/--host options and SANs first"

// Run executes every phase and prints the per-criterion results. It
// returns ErrNotAccepted unless every criterion passed, or the error that
// stopped the run.
func (r *Runner) Run(ctx context.Context) error {
	r.res.set(cMicroHost, NotRun, "%s", microHostGap)
	if err := r.preflight(ctx); err != nil {
		r.res.set(cPreflight, Fail, "%v", err)
		r.res.fillNotRun("blocked: preflight failed, nothing was deployed")
		r.cleanupRemote(ctx)
		return r.res.print(r.out)
	}
	if r.phaseDeploy(ctx) {
		r.phaseWorkloads(ctx)
		r.phaseStreaming(ctx)
		r.phaseMesh(ctx)
		r.phaseWatchdog(ctx)
	}
	r.phaseRm(ctx)
	r.cleanupRemote(ctx)
	r.res.fillNotRun("not reached: blocked by an earlier failure above")
	return r.res.print(r.out)
}

// ctlRun runs iormeshctl and logs the command.
func (r *Runner) ctlRun(ctx context.Context, args ...string) (string, error) {
	r.logf("  iormeshctl %s", strings.Join(args, " "))
	return r.ctl.Run(ctx, args...)
}

func (r *Runner) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.out, format+"\n", args...)
}

// phase prints a phase header.
func (r *Runner) phase(name string) { r.logf("\n== %s", name) }

// remoteBase is the run's directory on host (absolute: the workloads'
// --dir values appear in the traces and the golden's $tmp).
func (r *Runner) remoteBase(host string) string {
	return path.Join(r.info[host].home, remoteDir)
}

// cleanupRemote removes the run's own directory from both servers. The
// path is built from the home `pwd` printed, never from an empty one.
func (r *Runner) cleanupRemote(ctx context.Context) {
	for _, h := range []string{r.cfg.Host1, r.cfg.Host2} {
		if in := r.info[h]; in == nil || !strings.HasPrefix(in.home, "/") || len(in.home) < 2 {
			continue
		}
		if _, err := r.remote(ctx, h, "rm", "-rf", "--", r.remoteBase(h)); err != nil {
			r.logf("  warning: %s: removing %s: %v", h, r.remoteBase(h), err)
		}
	}
}
