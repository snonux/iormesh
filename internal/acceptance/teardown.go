package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"iormesh/internal/federate"
)

// Watchdog sizes: the floor sits watchdogMargin below what is free when
// the session starts, and the fill file takes twice that, so the trip
// is the run's own doing and comes within one 5 s check.
const (
	watchdogMargin  = 64 << 20
	watchdogMinFree = 1 << 30 // refuse a partition with less free
	fillName        = ".iormesh-m4-fill"
)

// phaseWatchdog proves the disk watchdog on HOST2 (M4-13).
func (r *Runner) phaseWatchdog(ctx context.Context) {
	r.phase("disk watchdog")
	detail, err := r.watchdog(ctx)
	r.res.setErr(cWatchdog, err, detail)
}

// watchdog starts a project whose -disk-min-free sits just below the
// free space, fills the partition past it, and checks the session ended
// with disk_limit, its last segment collected intact and the reason in
// the summary.
func (r *Runner) watchdog(ctx context.Context) (string, error) {
	h := r.cfg.Host2
	if err := r.startWatchdog(ctx, h); err != nil {
		return "", err
	}
	fill, err := r.fillPath(ctx, h)
	if err != nil {
		return "", err
	}
	// Remembered first, so cleanupRemote removes it after any failure or
	// interrupt; removed here as soon as the trip is seen, to give the
	// other sessions on the partition their space back.
	r.fillFile = fill
	if _, err := r.remote(ctx, h, "fallocate", "-l", strconv.Itoa(2*watchdogMargin), fill); err != nil {
		return "", err
	}
	if err := r.waitEnded(ctx, h, projDisk); err != nil {
		return "", err
	}
	_, _ = r.remote(ctx, h, "rm", "-f", "--", fill)
	if err := r.expectState(ctx, h, projDisk, "ended", "disk_limit"); err != nil {
		return "", err
	}
	if _, err := r.ctlRun(ctx, "collect", "-project", projDisk, h); err != nil {
		return "", err
	}
	return r.checkWatchdogBuild(ctx)
}

// startWatchdog adds and deploys the watchdog project. Its pattern
// matches no process: the session's own records (session, end) make the
// segment the trip must publish intact. -disk-limit 0 leaves only the
// floor, so the percentage cannot trip first on a fuller disk.
func (r *Runner) startWatchdog(ctx context.Context, h string) error {
	out, err := r.remote(ctx, h, "df", "-B1", "--output=avail", traceRoot)
	if err != nil {
		return err
	}
	ls := lines(out)
	avail, err := strconv.ParseInt(ls[len(ls)-1], 10, 64)
	if err != nil || avail < watchdogMinFree {
		return fmt.Errorf("%s: %q free on %s's partition, want >= 1 GiB (%v)", h, out, traceRoot, err)
	}
	floor := fmt.Sprintf("%dKiB", (avail-watchdogMargin)>>10)
	p := project{h, projDisk, "m4-watchdog-matches-no-process"}
	if err := r.add(ctx, p, "-disk-limit", "0", "-disk-min-free", floor); err != nil {
		return err
	}
	r.diskAdded = true
	if _, err := r.ctlRun(ctx, "deploy", "-project", projDisk, h); err != nil {
		return err
	}
	return r.expectState(ctx, h, projDisk, "running", "")
}

// fillPath picks the fill file's place: on the trace dir's filesystem
// (same st_dev), in a directory the SSH user may write.
func (r *Runner) fillPath(ctx context.Context, h string) (string, error) {
	dirs := []string{r.remoteBase(h), "/var/tmp"}
	if r.cfg.FillDir != "" {
		dirs = []string{r.cfg.FillDir}
	}
	want, err := r.remote(ctx, h, "stat", "-c", "%d", traceRoot)
	if err != nil {
		return "", err
	}
	for _, d := range dirs {
		if dev, err := r.remote(ctx, h, "stat", "-c", "%d", d); err == nil && dev == want {
			return path.Join(d, fillName), nil
		}
	}
	return "", fmt.Errorf("%s: none of %v is on %s's filesystem: set %s", h, dirs, traceRoot, EnvFillDir)
}

// waitEnded polls the session's state for up to a minute (the watchdog
// checks every 5 s).
func (r *Runner) waitEnded(ctx context.Context, h, proj string) error {
	for i := 0; i < 30; i++ {
		st, err := r.status(ctx, h, proj)
		if err != nil {
			return err
		}
		if st.State == "ended" {
			return nil
		}
		if err := r.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("%s: %s still running 60 s after the fill", h, proj)
}

// checkWatchdogBuild builds the watchdog project and finds its end
// reason in the model and in `show`'s summary, with no damaged segment.
func (r *Runner) checkWatchdogBuild(ctx context.Context) (string, error) {
	m, _, err := r.build(ctx, projDisk)
	if err != nil {
		return "", err
	}
	if len(m.Health.BadSegments) > 0 || m.Run.Segments == 0 {
		return "", fmt.Errorf("%d segments merged, damaged: %v", m.Run.Segments, m.Health.BadSegments)
	}
	if len(m.Health.Sessions) != 1 || m.Health.Sessions[0].End != "disk_limit" {
		return "", fmt.Errorf("sessions %+v: want one ended disk_limit", m.Health.Sessions)
	}
	show, err := r.ctlRun(ctx, "show", "-project", projDisk)
	if err != nil {
		return "", err
	}
	if !strings.Contains(show, "disk_limit") {
		return "", errors.New("`iormeshctl show` summary does not name disk_limit")
	}
	return fmt.Sprintf("ended disk_limit; %d segment(s) merged intact; summary names the reason", m.Run.Segments), nil
}

// phaseRm removes both hosts (M4-14) — also after a failed deploy, so a
// half-run leaves the servers as clean as a full one.
func (r *Runner) phaseRm(ctx context.Context) {
	r.phase("rm")
	detail, err := r.rmBoth(ctx)
	r.res.setErr(cRm, err, detail)
}

// rmBoth runs `rm HOST` for both and checks what is left on each server
// and in the registry, then that the store got every session's end.
func (r *Runner) rmBoth(ctx context.Context) (string, error) {
	if len(r.added) == 0 {
		return "", errors.New("nothing was added, so nothing to rm")
	}
	var errs []error
	for _, h := range []string{r.cfg.Host1, r.cfg.Host2} {
		if !r.added[h] {
			continue
		}
		if _, err := r.ctlRun(ctx, "rm", h); err != nil {
			errs = append(errs, err)
		}
	}
	for _, h := range []string{r.cfg.Host1, r.cfg.Host2} {
		errs = append(errs, r.checkGone(ctx, h))
	}
	if reg, err := federate.LoadRegistry(r.registryPath()); err != nil || len(reg.Servers) > 0 {
		errs = append(errs, fmt.Errorf("registry after rm: %d servers (%v)", len(reg.Servers), err))
	}
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	return r.checkAllEnded(ctx)
}

// checkGone fails when anything is left under /opt/iormesh or an iormesh
// unit is still loaded.
func (r *Runner) checkGone(ctx context.Context, h string) error {
	if there, err := r.remoteTest(ctx, h, "test", "-e", "/opt/iormesh"); err != nil || there {
		left, _ := r.remote(ctx, h, "find", "/opt/iormesh", "-maxdepth", "3")
		return fmt.Errorf("%s: /opt/iormesh still there: %s (%v)", h, strings.Join(lines(left), " "), err)
	}
	units, err := r.remote(ctx, h, "systemctl", "list-units", "--all", "--plain", "--no-legend", "iormesh-*")
	if err != nil || len(lines(units)) > 0 {
		return fmt.Errorf("%s: iormesh units left: %q (%v)", h, units, err)
	}
	return nil
}

// checkAllEnded builds every project of the run and requires each
// session's end in the store: rm synced off the last segments.
func (r *Runner) checkAllEnded(ctx context.Context) (string, error) {
	ps := r.meshProjects()
	if r.diskAdded {
		ps = append(ps, projDisk)
	}
	m, _, err := r.build(ctx, ps...)
	if err != nil {
		return "", err
	}
	for _, s := range m.Health.Sessions {
		if s.End == "" {
			return "", fmt.Errorf("session %s/%s on %s has no end in the store: not synced off", s.Project, s.ID, s.Host)
		}
	}
	if len(m.Health.BadSegments) > 0 {
		return "", fmt.Errorf("damaged segments: %v", m.Health.BadSegments)
	}
	return fmt.Sprintf("both hosts unregistered; nothing under /opt/iormesh; %d sessions ended and collected",
		len(m.Health.Sessions)), nil
}
