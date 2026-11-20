package segment

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"iormesh/internal/schema"
)

const gib = 1 << 30

// fakeDisk is a Watchdog.Statfs whose usage a test sets.
type fakeDisk struct{ u Usage }

func (d *fakeDisk) statfs(string) (Usage, error) { return d.u, nil }

func TestWatchdogCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		u    Usage
		pct  float64
		free uint64
		trip bool
	}{
		"below both":      {Usage{Used: 50 * gib, Avail: 50 * gib}, 90, gib, false},
		"at the percent":  {Usage{Used: 90 * gib, Avail: 10 * gib}, 90, gib, true},
		"under the floor": {Usage{Used: 1 * gib, Avail: gib / 2}, 90, gib, true},
		"percent off":     {Usage{Used: 99 * gib, Avail: 2 * gib}, 0, gib, false},
		"floor off":       {Usage{Used: 1, Avail: 1}, 90, 0, false},
		"empty fs":        {Usage{}, 90, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			d := &fakeDisk{u: tc.u}
			err := Watchdog{Dir: "/x", MaxUsedPercent: tc.pct, MinFreeBytes: tc.free,
				Statfs: d.statfs}.Check()
			if errors.Is(err, ErrDiskLimit) != tc.trip {
				t.Fatalf("Check = %v, want trip %v", err, tc.trip)
			}
		})
	}
}

func TestWatchdogStatfsError(t *testing.T) {
	err := Watchdog{Dir: filepath.Join(t.TempDir(), "missing"), MaxUsedPercent: 90}.Check()
	if err == nil || errors.Is(err, ErrDiskLimit) {
		t.Fatalf("Check of a missing dir = %v, want a statfs error", err)
	}
}

func TestStatfsReal(t *testing.T) {
	u, err := Statfs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if u.Used+u.Avail == 0 || u.UsedPercent() > 100 {
		t.Fatalf("implausible usage %+v", u)
	}
}

// TestWatchdogTripEndsCleanly is the disk-limit path: no trip below the
// limit, a trip once the fake disk fills, no rotation into a full disk,
// and still a whole last segment holding the end event.
func TestWatchdogTripEndsCleanly(t *testing.T) {
	disk := &fakeDisk{u: Usage{Used: 50 * gib, Avail: 50 * gib}}
	wd := &Watchdog{MaxUsedPercent: 90, MinFreeBytes: gib, Statfs: disk.statfs}
	w, dir := newTestWriter(t, func(c *Config) { c.Watchdog = wd })
	evs := richEvents()
	writeAll(t, w, evs[:len(evs)-1])
	if err := w.Tick(t0.Add(DefaultCheckEvery)); err != nil {
		t.Fatalf("below the limit: %v", err)
	}
	disk.u = Usage{Used: 95 * gib, Avail: 5 * gib}
	// Not yet due for a check: the ticker interval is honoured.
	if err := w.Tick(t0.Add(DefaultCheckEvery + time.Second)); err != nil {
		t.Fatalf("check before it was due: %v", err)
	}
	// Due for rotation, but the check before it trips: no new segment.
	if err := w.Tick(t0.Add(time.Minute)); !errors.Is(err, ErrDiskLimit) {
		t.Fatalf("Tick over the limit = %v, want ErrDiskLimit", err)
	}
	if got := readyFiles(t, dir); len(got) != 0 {
		t.Fatalf("rotated into a full disk: %v", got)
	}
	writeAll(t, w, evs[len(evs)-1:])
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	names := readyFiles(t, dir)
	if len(names) != 1 {
		t.Fatalf("ready/ = %v, want the one last segment", names)
	}
	got := readEvents(t, filepath.Join(dir, ReadyDir, names[0]))
	last := got[len(got)-1]
	if last.Event != schema.KindEnd || *last.Args.EndReason != schema.EndDiskLimit {
		t.Fatalf("last event %+v, want end disk_limit", last)
	}
}

func TestStartupOverLimitFails(t *testing.T) {
	disk := &fakeDisk{u: Usage{Used: 95 * gib, Avail: 5 * gib}}
	dir := filepath.Join(t.TempDir(), "shop")
	_, err := New(Config{Dir: dir, FQDN: "h", Session: "s1", Project: "shop",
		Watchdog: &Watchdog{MaxUsedPercent: 90, Statfs: disk.statfs}})
	if !errors.Is(err, ErrDiskLimit) {
		t.Fatalf("New over the limit = %v, want ErrDiskLimit", err)
	}
	// Failing fast means touching nothing: no directory was created.
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project dir created before the check: %v", err)
	}
}
