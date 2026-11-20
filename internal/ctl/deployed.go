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

// deployedRecord is the start argv deploy last launched per host and
// project. A live session keeps its flags until it is restarted (decision
// #19), and neither its unit nor state.json says which flags it got, so
// the controller remembers them: a re-deploy with the same registry entry
// leaves a running session alone, a changed entry restarts it. It is a
// hint, not a trust boundary — a stale or edited record only causes, or
// skips, one restart — so it is kept private (0600) but not vetted like
// the registry.
type deployedRecord map[string]map[string][]string

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

// same reports whether argv is what deploy last started for the project.
func (r deployedRecord) same(host, project string, argv []string) bool {
	last, ok := r[host][project]
	return ok && slices.Equal(last, argv)
}

// set records argv as the project's last start.
func (r deployedRecord) set(host, project string, argv []string) {
	if r[host] == nil {
		r[host] = map[string][]string{}
	}
	r[host][project] = slices.Clone(argv)
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
