package routemap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/k8sname"
)

// The discovery snapshot: what `iormeshctl routes discover` read from
// the cluster, kept beside the service database so `build` stays offline
// (it never runs kubectl) and a rebuild over the same snapshot writes
// the same bytes. Rediscovering replaces it.

// SnapshotName is the snapshot's file name, beside services.db.
const SnapshotName = "routes-discovered.json"

// maxSnapshotSize bounds what LoadSnapshot reads: the largest valid map
// with its flags.
const maxSnapshotSize = 4 * maxFileSize

// maxFlags bounds a snapshot's flags, as maxRoutes bounds its routes.
const maxFlags = 4096

// Snapshot is one discovery run: when, against which context and
// namespaces, and what it found.
type Snapshot struct {
	Taken      time.Time `json:"taken"`
	Context    string    `json:"context"`
	Namespaces []string  `json:"namespaces"`
	Map        Map       `json:"map"`
	Flags      []Flag    `json:"flags"`
}

// SnapshotPath is the snapshot beside the service database.
func SnapshotPath(servicedb string) string {
	return filepath.Join(filepath.Dir(servicedb), SnapshotName)
}

// validate checks a snapshot read back: the map by its own rules, the
// context and namespaces by Kubernetes', and the flags' count. Flag
// texts are printed through termsafe, like every string the model holds.
func (s *Snapshot) validate() error {
	if !k8sname.Context(s.Context) || len(s.Namespaces) == 0 || len(s.Flags) > maxFlags {
		return fmt.Errorf("%w: snapshot context, namespaces or flag count", ErrInvalid)
	}
	for _, ns := range s.Namespaces {
		if !k8sname.Label(ns) {
			return fmt.Errorf("%w: snapshot namespace %q", ErrInvalid, ns)
		}
	}
	return s.Map.Validate()
}

// SaveSnapshot writes s to path atomically, 0600 in a 0700 directory.
func SaveSnapshot(path string, s Snapshot) error {
	if err := s.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(data, '\n'))
}

// LoadSnapshot reads the snapshot at path; a missing file is none (nil,
// no error). It is held to the static map's rules: private to this user,
// bounded, no unknown fields, valid.
func LoadSnapshot(path string) (*Snapshot, error) {
	data, err := federate.ReadPrivate(path, maxSnapshotSize, ErrUnsafeFile, ErrTooLarge)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("route snapshot: %w", err)
	}
	var s Snapshot
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("route snapshot %s: %w", path, err)
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("route snapshot %s: %w", path, err)
	}
	return &s, nil
}

// writePrivate writes data to path atomically, 0600 in a 0700 directory:
// a temp file in the same directory, synced, then renamed over the old
// one, so a crash leaves the old file or the new one.
func writePrivate(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".routes-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
