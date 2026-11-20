package federate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"iormesh/internal/project"
	"iormesh/internal/target"
)

// The server registry (PLAN.md M4 "Server registry", decisions #12, #21):
// the controller's list of hosts it may deploy to, kept in
// ~/.config/iormesh/servers.yaml. Only registered hosts are ever deployed
// to (principle #3), so the file is the controller's trust boundary: it is
// written 0600 in a 0700 directory and refused when others could write it.

// Registry errors, for errors.Is.
var (
	ErrDuplicate      = errors.New("registry: already registered")
	ErrUnknownHost    = errors.New("registry: host not registered")
	ErrUnknownProject = errors.New("registry: project not registered on host")
	ErrInvalid        = errors.New("registry: invalid entry")
	ErrUnsafeFile     = errors.New("registry: file or directory not private to its owner")
	ErrTooLarge       = errors.New("registry: file too large")
)

// maxRegistrySize bounds what LoadRegistry reads: a registry of thousands
// of hosts is a few hundred KiB, and anything past 1 MiB is not one.
const maxRegistrySize = 1 << 20

// hostName is what `iormeshctl add` accepts: an optional user@ and a host
// name, IPv4 address or ~/.ssh/config alias. It reaches ssh and rsync as an
// argv element, so a leading dash (an option: "-oProxyCommand=…") and
// anything a shell or rsync's host:path syntax would read (space, quote,
// colon, slash) are excluded. IPv6 literals need an ssh_config alias.
var hostName = regexp.MustCompile(
	`^(?:[A-Za-z0-9_][A-Za-z0-9._-]{0,63}@)?[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

// Registry is the parsed servers.yaml. Every server carries at least one
// project: a host is registered to trace something, and a server without a
// project could not be started (#21: the project is a manual label kept
// with the registry entry).
type Registry struct {
	Servers []Server `yaml:"servers"`
}

// Server is one registered host and the independent sessions, one per
// project, it hosts.
type Server struct {
	Host     string    `yaml:"host"`
	Projects []Project `yaml:"projects"`
}

// Project is one session on a server: its label, what it traces, and the
// tracer flags that pass through on deploy (PLAN.md M4 "No management
// plane"). The flags are stored verbatim; deploy hands them to the tracer
// as separate argv elements, never as a shell string.
type Project struct {
	Name   string   `yaml:"name"`
	Target Target   `yaml:"target"`
	Flags  []string `yaml:"flags,omitempty"`
}

// Target names the processes a project traces. PIDs are deliberately not
// offered: they do not survive a restart, and a registry entry outlives
// many sessions. Exactly one field is set (target.Spec's rule).
type Target struct {
	Pattern    string `yaml:"pattern,omitempty"`
	ListenPort uint16 `yaml:"listen_port,omitempty"`
}

// Spec converts t to the tracer's target spec for validation.
func (t Target) Spec() target.Spec {
	return target.Spec{Pattern: t.Pattern, ListenPort: t.ListenPort}
}

// ValidHost reports whether host is usable as a registry host name.
func ValidHost(host string) bool {
	return hostName.MatchString(host)
}

// DefaultRegistryPath is ~/.config/iormesh/servers.yaml (decision #12), or
// the same under $XDG_CONFIG_HOME. os.UserConfigDir is not used: on macOS
// it is ~/Library/Application Support, and PLAN.md names one path for
// every controller platform.
func DefaultRegistryPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("registry path: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "iormesh", "servers.yaml"), nil
}

// LoadRegistry reads the registry at path. A missing file is an empty
// registry: the first `add` creates it. Unknown keys are an error rather
// than dropped, so a later Save cannot silently lose a section a newer
// iormeshctl (or the operator) wrote — M6's expand allowlist, say.
func LoadRegistry(path string) (Registry, error) {
	data, err := readPrivate(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Registry{}, nil
	}
	if err != nil {
		return Registry{}, err
	}
	var r Registry
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil && !errors.Is(err, io.EOF) {
		return Registry{}, fmt.Errorf("registry %s: %w", path, err)
	}
	if err := r.Validate(); err != nil {
		return Registry{}, fmt.Errorf("registry %s: %w", path, err)
	}
	return r, nil
}

// Save writes r to path atomically: a temp file (0600) in the same 0700
// directory, synced, then renamed over the old one, so a crash leaves the
// old registry or the new one, never half of each. Two concurrent
// iormeshctl runs can still lose one's update (last rename wins); the
// registry is edited by one operator at a time.
func (r *Registry) Save(path string) error {
	if err := r.Validate(); err != nil {
		return err
	}
	// Two-space indent, as the operator would write it by hand: the file
	// is meant to be read (yaml.v3 defaults to four).
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	// A directory that pre-existed may be open to others; MkdirAll does
	// not touch it, so check before writing into it.
	if err := checkRegistryDir(dir); err != nil {
		return err
	}
	return writeAtomic(dir, path, buf.Bytes())
}

// Validate checks every entry and that hosts, and projects per host, are
// unique: the same rules Add enforces, applied to a hand-edited file too.
func (r *Registry) Validate() error {
	seen := map[string]bool{}
	for _, s := range r.Servers {
		if seen[s.Host] {
			return fmt.Errorf("%w: host %q listed twice", ErrInvalid, s.Host)
		}
		seen[s.Host] = true
		if err := s.validate(); err != nil {
			return err
		}
	}
	return nil
}

// Add registers project p on host, creating the host entry if needed. The
// same host and project twice is ErrDuplicate: a second session under one
// label would share its trace directory and systemd unit.
func (r *Registry) Add(host string, p Project) error {
	if !ValidHost(host) {
		return fmt.Errorf("%w: host %q", ErrInvalid, host)
	}
	if err := p.validate(); err != nil {
		return err
	}
	i := r.find(host)
	if i < 0 {
		r.Servers = append(r.Servers, Server{Host: host, Projects: []Project{p}})
		return nil
	}
	if r.Servers[i].findProject(p.Name) >= 0 {
		return fmt.Errorf("%w: project %q on %s", ErrDuplicate, p.Name, host)
	}
	r.Servers[i].Projects = append(r.Servers[i].Projects, p)
	return nil
}

// Remove unregisters host, or only its project name when name is set. A
// host whose last project goes is dropped too, keeping Validate's rule
// that every server has a project.
func (r *Registry) Remove(host, name string) error {
	i := r.find(host)
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrUnknownHost, host)
	}
	if name != "" {
		s := &r.Servers[i]
		j := s.findProject(name)
		if j < 0 {
			return fmt.Errorf("%w: %q on %s", ErrUnknownProject, name, host)
		}
		s.Projects = slices.Delete(s.Projects, j, j+1)
		if len(s.Projects) > 0 {
			return nil
		}
	}
	r.Servers = slices.Delete(r.Servers, i, i+1)
	return nil
}

// find returns the index of host's entry, or -1.
func (r *Registry) find(host string) int {
	return slices.IndexFunc(r.Servers, func(s Server) bool { return s.Host == host })
}

func (s Server) validate() error {
	if !ValidHost(s.Host) {
		return fmt.Errorf("%w: host %q", ErrInvalid, s.Host)
	}
	if len(s.Projects) == 0 {
		return fmt.Errorf("%w: host %s has no project", ErrInvalid, s.Host)
	}
	seen := map[string]bool{}
	for _, p := range s.Projects {
		if seen[p.Name] {
			return fmt.Errorf("%w: project %q listed twice on %s", ErrInvalid, p.Name, s.Host)
		}
		seen[p.Name] = true
		if err := p.validate(); err != nil {
			return fmt.Errorf("%s: %w", s.Host, err)
		}
	}
	return nil
}

// findProject returns the index of project name on s, or -1.
func (s Server) findProject(name string) int {
	return slices.IndexFunc(s.Projects, func(p Project) bool { return p.Name == name })
}

func (p Project) validate() error {
	if !project.Valid(p.Name) {
		return fmt.Errorf("%w: project %q: want %s", ErrInvalid, p.Name, project.Rule)
	}
	if err := p.Target.Spec().Validate(); err != nil {
		return fmt.Errorf("%w: project %q: %w", ErrInvalid, p.Name, err)
	}
	// Pattern and flags reach the server's login shell (quoted, see
	// SSH.Run); control characters have no use there, and a newline is
	// where non-POSIX shells stop honoring the quotes.
	for _, v := range append([]string{p.Target.Pattern}, p.Flags...) {
		if strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return fmt.Errorf("%w: project %q: control character in %q", ErrInvalid, p.Name, v)
		}
	}
	return nil
}

// readPrivate reads path after checking that nobody but this user owns or
// can write it or the directory holding it: a registry someone else can edit, or
// replace by renaming over it, lets them choose where the operator's key
// deploys a root-run tracer. The read is bounded by maxRegistrySize.
func readPrivate(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("registry %s: %w", path, err)
	}
	if err := checkOwned(info, path, 0o022, ErrUnsafeFile); err != nil {
		return nil, err
	}
	if err := checkRegistryDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRegistrySize+1))
	if err != nil {
		return nil, fmt.Errorf("registry %s: %w", path, err)
	}
	if len(data) > maxRegistrySize {
		return nil, fmt.Errorf("%w: %s is over %d bytes", ErrTooLarge, path, maxRegistrySize)
	}
	return data, nil
}

// writeAtomic is Save's temp-file-and-rename step. os.CreateTemp creates
// the file 0600, which the rename carries over to path.
func writeAtomic(dir, path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(dir, ".servers-*.yaml")
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("registry: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("registry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	return syncDir(dir)
}

// syncDir fsyncs dir so the rename itself is durable: without it a crash
// can bring back the old directory entry, and the add or rm is lost.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("registry: sync %s: %w", dir, err)
	}
	return nil
}
