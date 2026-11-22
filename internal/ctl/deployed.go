package ctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"iormesh/internal/servicedb"
)

// deployedFileName is deploy's record of what it last started, next to the
// service database in ~/.local/share/iormesh.
const deployedFileName = "deployed.json"

// maxDeployedSize bounds the record read: one start argv per host and
// project is a few hundred bytes.
const maxDeployedSize = 1 << 20

// deployedRecord is what deploy last started per host and project: the
// start argv and the build version it ran. A live session keeps its flags
// and its binary until it is restarted (decision #19) — upgrade replaces
// the file, not the running process. state.json records the run's version
// and argv since task ph4 (iormeshctl status shows them), but the server
// writes it, and a tracer from before then does not: deploy decides on
// what the controller remembers itself. A re-deploy of the same build and registry entry leaves
// a running session alone, a changed entry or a new build (possibly a
// security fix) restarts it. rm also reads it: its projects of a host,
// tombstones included, are ones rm's sweep may collect (task hi4). Only
// the operator writes it, never a server, so it is a hint, not a trust
// boundary against the servers — a stale or edited record only causes, or
// skips, one restart or one collect — and it is kept private (0600) but
// not vetted like the registry.
type deployedRecord map[string]map[string]deployedSession

// deployedSession is one project's last start, or a tombstone.
type deployedSession struct {
	Start   []string `json:"start"`
	Version string   `json:"version"`
	// Dropped marks a tombstone: rm dropped the project from the
	// registry while its host was unreachable, so its tracer and
	// segments are still there and a later rm of the host must still
	// collect it. A tombstone has no start or version, so a re-add and
	// deploy restarts the project (and set replaces the tombstone).
	Dropped bool `json:"dropped,omitempty"`
}

// defaultDeployedPath is ~/.local/share/iormesh/deployed.json.
func defaultDeployedPath() (string, error) {
	db, err := servicedb.DefaultPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(db), deployedFileName), nil
}

// loadDeployed reads the record; a missing file is an empty one.
func loadDeployed(path string) (deployedRecord, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return deployedRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("deploy record: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxDeployedSize+1))
	if err != nil {
		return nil, fmt.Errorf("deploy record: %w", err)
	}
	if len(data) > maxDeployedSize {
		return nil, fmt.Errorf("deploy record %s: over %d bytes", path, maxDeployedSize)
	}
	r := deployedRecord{}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("deploy record %s: %w", path, err)
	}
	return r, nil
}

// same reports whether deploy last started the project with argv on
// build version. A tombstone matches nothing, so the project restarts.
func (r deployedRecord) same(host, project string, argv []string, version string) bool {
	last, ok := r[host][project]
	return ok && !last.Dropped && last.Version == version && slices.Equal(last.Start, argv)
}

// set records argv and version as the project's last start.
func (r deployedRecord) set(host, project string, argv []string, version string) {
	if r[host] == nil {
		r[host] = map[string]deployedSession{}
	}
	r[host][project] = deployedSession{Start: slices.Clone(argv), Version: version}
}

// forget drops host, or only its project when project is set, as
// iormeshctl rm drops them from the registry. It reports whether
// anything was dropped.
func (r deployedRecord) forget(host, project string) bool {
	if _, ok := r[host]; !ok {
		return false
	}
	if project == "" {
		delete(r, host)
		return true
	}
	if _, ok := r[host][project]; !ok {
		return false
	}
	delete(r[host], project)
	if len(r[host]) == 0 {
		delete(r, host)
	}
	return true
}

// drop turns each of host's projects into a tombstone (see
// deployedSession.Dropped), as rm does when it unregisters them from an
// unreachable host. It reports whether the record changed.
func (r deployedRecord) drop(host string, projects []string) bool {
	changed := false
	for _, p := range projects {
		if last, ok := r[host][p]; ok && last.Dropped {
			continue
		}
		if r[host] == nil {
			r[host] = map[string]deployedSession{}
		}
		r[host][p] = deployedSession{Dropped: true}
		changed = true
	}
	return changed
}

// save writes the record atomically (temp file, rename) into a 0700
// directory: it names the operator's hosts and their targets.
func (r deployedRecord) save(path string) (err error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("deploy record: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("deploy record: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".deployed-*.json")
	if err != nil {
		return fmt.Errorf("deploy record: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	_, werr := tmp.Write(append(data, '\n'))
	if err := errors.Join(werr, tmp.Sync(), tmp.Close()); err != nil {
		return fmt.Errorf("deploy record: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("deploy record: %w", err)
	}
	return nil
}
