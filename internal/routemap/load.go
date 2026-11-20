package routemap

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"iormesh/internal/federate"
)

// Load errors, for errors.Is.
var (
	ErrUnsafeFile = errors.New("routemap: file or directory not private to its owner")
	ErrTooLarge   = errors.New("routemap: file too large")
)

// maxFileSize bounds what Load reads. The largest map maxRoutes and
// maxServices allow is a few MiB of YAML; a file past 4 MiB is not a
// route map, and the bound also caps what a YAML alias bomb can expand
// from.
const maxFileSize = 4 << 20

// FileName is the static map's name, next to the registry.
const FileName = "routes.yaml"

// DefaultPath is routes.yaml in the registry's directory
// (~/.config/iormesh, decision #15): the map is the operator's, kept
// with the registry and under its rules.
func DefaultPath(registry string) string {
	return filepath.Join(filepath.Dir(registry), FileName)
}

// Load reads the static map at path. A missing file is no map (nil, no
// error): the route map is optional. The file is held to the registry's
// rules — this user's, written by nobody else, its directory too — since
// it decides what a build says an unresolved edge reached; unknown keys
// are an error, as in the registry, so a typo never silently drops a
// route.
func Load(path string) (*Map, error) {
	data, err := federate.ReadPrivate(path, maxFileSize, ErrUnsafeFile, ErrTooLarge)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("route map: %w", err)
	}
	return Parse(path, data)
}

// Parse decodes and validates one static map's YAML; name labels errors.
// One document only: a second would be silently ignored by Decode.
func Parse(name string, data []byte) (*Map, error) {
	var m Map
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("route map %s: %w", name, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: route map %s: more than one YAML document", ErrInvalid, name)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("route map %s: %w", name, err)
	}
	return &m, nil
}
