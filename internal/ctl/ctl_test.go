package ctl

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"iormesh/internal/federate"
)

// testEnv points the default registry at a temp file; the operator's
// ~/.config is never touched. Its hosts answer but have no tracer
// installed, so rm only edits the registry.
func testEnv(t *testing.T) (env, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "iormesh", "servers.yaml")
	deployed := filepath.Join(t.TempDir(), "share", deployedFileName)
	store := filepath.Join(t.TempDir(), "traces")
	e := env{stdout: &stdout, stderr: &stderr,
		registry: func() (string, error) { return path, nil },
		deployed: func() (string, error) { return deployed, nil },
		traces:   func() (string, error) { return store, nil },
		transport: func(host string) (federate.Transport, error) {
			return fakeTransport{host: host, log: &[][]string{}, respond: func(_ string, argv []string) ([]byte, error) {
				if argv[0] == "test" {
					return nil, errors.New("exit status 1")
				}
				return nil, nil
			}}, nil
		}}
	return e, &stdout, &stderr, path
}

// TestAddRm walks add → add → rm -project → rm through the CLI and checks
// the file after each step.
func TestAddRm(t *testing.T) {
	e, stdout, stderr, path := testEnv(t)
	steps := []struct {
		args []string
		want []federate.Server
	}{
		{[]string{"add", "-project", "shop", "-pattern", "*orders*", "app1", "--", "-duration", "1h"},
			[]federate.Server{{Host: "app1", Projects: []federate.Project{
				{Name: "shop", Target: federate.Target{Pattern: "*orders*"}, Flags: []string{"-duration", "1h"}}}}}},
		{[]string{"add", "-project", "billing", "-listen-port", "8443", "app1"},
			[]federate.Server{{Host: "app1", Projects: []federate.Project{
				{Name: "shop", Target: federate.Target{Pattern: "*orders*"}, Flags: []string{"-duration", "1h"}},
				{Name: "billing", Target: federate.Target{ListenPort: 8443}}}}}},
		{[]string{"rm", "-project", "shop", "app1"},
			[]federate.Server{{Host: "app1", Projects: []federate.Project{
				{Name: "billing", Target: federate.Target{ListenPort: 8443}}}}}},
		{[]string{"rm", "app1"}, []federate.Server{}},
	}
	for _, s := range steps {
		if code := run(s.args, e); code != exitOK {
			t.Fatalf("%v: exit %d, stderr %s", s.args, code, stderr)
		}
		r, err := federate.LoadRegistry(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(r.Servers, s.want) {
			t.Errorf("after %v:\n got %+v\nwant %+v", s.args, r.Servers, s.want)
		}
	}
	if !strings.Contains(stdout.String(), "added app1 project shop") {
		t.Errorf("stdout: %q", stdout)
	}
}

// TestCommandErrors: each failure exits non-zero, says why, and leaves
// the registry as it was.
func TestCommandErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
		msg  string
	}{
		{"duplicate add", []string{"add", "-project", "shop", "-pattern", "java", "app1"}, exitError, "already registered"},
		{"rm unknown host", []string{"rm", "nope"}, exitError, "host not registered"},
		{"rm unknown project", []string{"rm", "-project", "nope", "app1"}, exitError, "project not registered"},
		{"no host", []string{"add", "-project", "x", "-pattern", "java"}, exitUsage, "needs a HOST"},
		{"no project", []string{"add", "-pattern", "java", "app2"}, exitError, "invalid entry"},
		{"option host", []string{"add", "-project", "x", "-pattern", "java", "-oProxyCommand=id"}, exitUsage, "flag provided but not defined"},
		{"option host after --", []string{"add", "-project", "x", "-pattern", "java", "--", "-oProxyCommand=id"}, exitError, "invalid entry"},
		{"flag after host", []string{"add", "-pattern", "java", "app2", "-project", "x"}, exitUsage, "go before HOST"},
		{"owned tracer flag", []string{"add", "-project", "x", "-pattern", "java", "app2", "--out=/tmp"}, exitUsage, "go before HOST"},
		{"port range", []string{"add", "-project", "x", "-listen-port", "70000", "app2"}, exitUsage, "out of range"},
		{"rm two hosts", []string{"rm", "a", "b"}, exitUsage, "exactly one HOST"},
		{"unknown subcommand", []string{"frobnicate"}, exitUsage, "unknown subcommand"},
		{"deploy without host", []string{"deploy"}, exitUsage, "one or more HOSTs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _, stderr, path := testEnv(t)
			if code := run([]string{"add", "-project", "shop", "-pattern", "java", "app1"}, e); code != exitOK {
				t.Fatalf("setup add: exit %d: %s", code, stderr)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if code := run(c.args, e); code != c.code || !strings.Contains(stderr.String(), c.msg) {
				t.Errorf("exit %d, stderr %q; want %d with %q", code, stderr, c.code, c.msg)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Errorf("registry changed:\n%s", after)
			}
		})
	}
}

func TestRegistryFlagOverridesDefault(t *testing.T) {
	e, _, stderr, defaultPath := testEnv(t)
	other := filepath.Join(t.TempDir(), "other.yaml")
	if code := run([]string{"add", "-registry", other, "-project", "p", "-pattern", "java", "h"}, e); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("-registry file not written: %v", err)
	}
	if _, err := os.Stat(defaultPath); !os.IsNotExist(err) {
		t.Errorf("default registry touched: %v", err)
	}
}

func TestUsage(t *testing.T) {
	e, stdout, stderr, _ := testEnv(t)
	if code := run(nil, e); code != exitUsage || !strings.Contains(stderr.String(), "iormeshctl add") {
		t.Errorf("no args: exit %d, stderr %q", code, stderr)
	}
	if code := run([]string{"help"}, e); code != exitOK || !strings.Contains(stdout.String(), "iormeshctl rm") {
		t.Errorf("help: exit %d, stdout %q", code, stdout)
	}
}
