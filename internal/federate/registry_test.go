package federate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func shop() Project {
	return Project{Name: "shop", Target: Target{Pattern: "*java*orders-service*"},
		Flags: []string{"-duration", "4h"}}
}

// TestRegistryRoundTrip: what Save writes, LoadRegistry reads back
// unchanged, with the file 0600 in a 0700 directory it created.
func TestRegistryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iormesh", "servers.yaml")
	var r Registry
	for _, add := range []struct {
		host string
		p    Project
	}{
		{"app1.example.org", shop()},
		{"app1.example.org", Project{Name: "billing", Target: Target{ListenPort: 8443}}},
		{"ops@10.0.0.7", shop()},
	} {
		if err := r.Add(add.host, add.p); err != nil {
			t.Fatalf("Add(%s, %s): %v", add.host, add.p.Name, err)
		}
	}
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, r)
	}
	assertMode(t, path, 0o600)
	assertMode(t, filepath.Dir(path), 0o700)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s: mode %v, want %v", path, got, want)
	}
}

func TestLoadRegistryMissingIsEmpty(t *testing.T) {
	r, err := LoadRegistry(filepath.Join(t.TempDir(), "servers.yaml"))
	if err != nil || len(r.Servers) != 0 {
		t.Fatalf("LoadRegistry(missing) = %+v, %v; want empty, nil", r, err)
	}
}

// TestLoadRegistryRejects covers files LoadRegistry must refuse rather
// than act on.
func TestLoadRegistryRejects(t *testing.T) {
	cases := []struct {
		name, yaml string
		mode       os.FileMode
		want       error
	}{
		{"group writable", "servers: []\n", 0o620, ErrUnsafeFile},
		{"world writable", "servers: []\n", 0o602, ErrUnsafeFile},
		{"unknown key", "servers: []\nexpand: {max_nodes: 3}\n", 0o600, nil},
		{"duplicate host", "servers:\n- {host: a, projects: [{name: p, target: {pattern: x}}]}\n" +
			"- {host: a, projects: [{name: q, target: {pattern: x}}]}\n", 0o600, ErrInvalid},
		{"no project", "servers:\n- {host: a, projects: []}\n", 0o600, ErrInvalid},
		{"option host", "servers:\n- {host: -oProxyCommand=x, projects: [{name: p, target: {pattern: x}}]}\n",
			0o600, ErrInvalid},
		{"two targets", "servers:\n- {host: a, projects: [{name: p, target: {pattern: x, listen_port: 80}}]}\n",
			0o600, ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "servers.yaml")
			if err := os.WriteFile(path, []byte(c.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			// Chmod, not WriteFile's mode: the umask would mask it.
			if err := os.Chmod(path, c.mode); err != nil {
				t.Fatal(err)
			}
			_, err := LoadRegistry(path)
			if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
				t.Errorf("LoadRegistry = %v, want %v", err, c.want)
			}
		})
	}
}

// TestRegistryDirAndSize: a directory others can write (they could rename
// their own file over servers.yaml) is refused by Load and Save alike, and
// so is a file over maxRegistrySize.
func TestRegistryDirAndSize(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "iormesh")
	path := filepath.Join(dir, "servers.yaml")
	var r Registry
	if err := r.Add("app1", shop()); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(path); !errors.Is(err, ErrUnsafeFile) {
		t.Errorf("Load in a group-writable dir = %v, want ErrUnsafeFile", err)
	}
	if err := r.Save(path); !errors.Is(err, ErrUnsafeFile) {
		t.Errorf("Save in a group-writable dir = %v, want ErrUnsafeFile", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // readable is fine
		t.Fatal(err)
	}
	big := append([]byte("# "), make([]byte, maxRegistrySize)...)
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(path); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Load of an oversized file = %v, want ErrTooLarge", err)
	}
}

func TestAddDuplicate(t *testing.T) {
	var r Registry
	if err := r.Add("app1", shop()); err != nil {
		t.Fatal(err)
	}
	if err := r.Add("app1", shop()); !errors.Is(err, ErrDuplicate) {
		t.Errorf("second Add = %v, want ErrDuplicate", err)
	}
	if len(r.Servers) != 1 || len(r.Servers[0].Projects) != 1 {
		t.Errorf("duplicate Add changed the registry: %+v", r)
	}
}

// TestAddRejects: hosts and projects that must never reach ssh.
func TestAddRejects(t *testing.T) {
	cases := []struct {
		name, host string
		p          Project
	}{
		{"option host", "-oProxyCommand=touch /tmp/x", shop()},
		{"shell host", "a;rm -rf /", shop()},
		{"space host", "a b", shop()},
		{"path host", "a:/etc", shop()},
		{"empty host", "", shop()},
		{"bad project", "app1", Project{Name: "-x", Target: Target{Pattern: "java"}}},
		{"no target", "app1", Project{Name: "shop"}},
		{"too broad", "app1", Project{Name: "shop", Target: Target{Pattern: "*"}}},
		{"newline flag", "app1", Project{Name: "shop", Target: Target{Pattern: "java"},
			Flags: []string{"-duration", "1h\nid"}}},
		{"control pattern", "app1", Project{Name: "shop", Target: Target{Pattern: "java\x1b"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var r Registry
			if err := r.Add(c.host, c.p); !errors.Is(err, ErrInvalid) {
				t.Errorf("Add(%q, %+v) = %v, want ErrInvalid", c.host, c.p, err)
			}
		})
	}
}

func TestRemove(t *testing.T) {
	billing := Project{Name: "billing", Target: Target{ListenPort: 8443}}
	cases := []struct {
		name, host, project string
		want                error
		left                []string // "host/project" after Remove
	}{
		{"unknown host", "nope", "", ErrUnknownHost, []string{"a/shop", "a/billing", "b/shop"}},
		{"unknown project", "a", "nope", ErrUnknownProject, []string{"a/shop", "a/billing", "b/shop"}},
		{"whole host", "a", "", nil, []string{"b/shop"}},
		{"one project", "a", "billing", nil, []string{"a/shop", "b/shop"}},
		{"last project drops host", "b", "shop", nil, []string{"a/shop", "a/billing"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Registry{Servers: []Server{
				{Host: "a", Projects: []Project{shop(), billing}},
				{Host: "b", Projects: []Project{shop()}},
			}}
			if err := r.Remove(c.host, c.project); !errors.Is(err, c.want) {
				t.Fatalf("Remove = %v, want %v", err, c.want)
			}
			if got := entries(r); !reflect.DeepEqual(got, c.left) {
				t.Errorf("left %v, want %v", got, c.left)
			}
		})
	}
}

func entries(r Registry) []string {
	var out []string
	for _, s := range r.Servers {
		for _, p := range s.Projects {
			out = append(out, s.Host+"/"+p.Name)
		}
	}
	return out
}

func TestDefaultRegistryPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	if got, err := DefaultRegistryPath(); err != nil || got != "/cfg/iormesh/servers.yaml" {
		t.Errorf("with XDG_CONFIG_HOME: %q, %v", got, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/op")
	if got, err := DefaultRegistryPath(); err != nil || got != "/home/op/.config/iormesh/servers.yaml" {
		t.Errorf("without XDG_CONFIG_HOME: %q, %v", got, err)
	}
}
