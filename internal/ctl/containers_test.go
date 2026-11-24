package ctl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/viewmodel"
)

// Container names (task jk4): collect stores each pod's container id →
// name map as containers.json in the pod's store directory, and build
// names each process's container from it.

const (
	prodUID = "3b1c9a52-7e0d-4c1f-9a8e-2f6d5b4c3a21"
	devUID  = "9d8c7b6a-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
)

// writeContainers writes the container map of pod uid's store directory
// (project shop), as collect leaves it.
func writeContainers(t *testing.T, f *buildFixture, uid string, m map[string]string) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.store, "shop", "pod-"+uid, federate.ContainersFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// processesOf is the processes view, from show -json.
func processesOf(t *testing.T, out string) []viewmodel.Process {
	t.Helper()
	var ps []viewmodel.Process
	if err := json.Unmarshal([]byte(out), &ps); err != nil {
		t.Fatalf("processes -json: %v\n%s", err, out)
	}
	return ps
}

// TestBuildContainerNames: a pod session whose store directory holds the
// container map shows its process's container by name, in the model and
// in `show processes`; a session collected before the map (no file)
// shows the id alone; the map is not a stray store entry.
func TestBuildContainerNames(t *testing.T) {
	f := newBuildFixture(t)
	id := strings.Repeat("ab", 32)
	podClient(t, f, prodUID, "web-7d9f", "prod", "10.0.9.9:80")
	podClient(t, f, devUID, "web-1a2b", "dev", "10.0.8.8:80")
	writeContainers(t, f, prodUID, map[string]string{id: "app", strings.Repeat("cd", 32): "sidecar"})
	f.mustRun(t, "build")
	byHost := map[string]viewmodel.Process{}
	for _, p := range processesOf(t, f.mustRun(t, "show", "processes", "-json")) {
		byHost[p.Host] = p
	}
	named, old := byHost["pod-"+prodUID], byHost["pod-"+devUID]
	if named.ContainerID != id || named.Container != "app" {
		t.Errorf("mapped pod's process: container %q id %q, want app %s", named.Container, named.ContainerID, id)
	}
	if old.ContainerID != id || old.Container != "" {
		t.Errorf("unmapped pod's process: container %q id %q, want no name, id %s", old.Container, old.ContainerID, id)
	}
	text := f.mustRun(t, "show", "processes")
	if !strings.Contains(text, "container app ("+id[:12]+")") || !strings.Contains(text, "container "+id+"\n") {
		t.Errorf("show processes names no container (app, and the bare id):\n%s", text)
	}
	var health viewmodel.Health
	if err := json.Unmarshal([]byte(f.mustRun(t, "show", "health", "-json")), &health); err != nil {
		t.Fatal(err)
	}
	if len(health.Ignored) != 0 {
		t.Errorf("ignored %v: the container map is no stray entry", health.Ignored)
	}
}

// TestBuildContainerNamesBadMap: a container map that is not one (bad
// JSON, an id or a name a cluster would not issue) names nothing and
// fails no build: the ids stay, and health lists the file as ignored.
func TestBuildContainerNamesBadMap(t *testing.T) {
	id := strings.Repeat("ab", 32)
	for _, m := range []string{`{`, `{"` + id + `": "App\u001b[31m"}`, `{"` + strings.ToUpper(id) + `": "app"}`} {
		f := newBuildFixture(t)
		podClient(t, f, prodUID, "web-7d9f", "prod", "10.0.9.9:80")
		path := filepath.Join(f.store, "shop", "pod-"+prodUID, federate.ContainersFile)
		if err := os.WriteFile(path, []byte(m), 0o600); err != nil {
			t.Fatal(err)
		}
		f.mustRun(t, "build")
		for _, p := range processesOf(t, f.mustRun(t, "show", "processes", "-json")) {
			if p.Host == "pod-"+prodUID && (p.Container != "" || p.ContainerID != id) {
				t.Errorf("map %q: container %q id %q, want the id %s unnamed", m, p.Container, p.ContainerID, id)
			}
		}
		var health viewmodel.Health
		if err := json.Unmarshal([]byte(f.mustRun(t, "show", "health", "-json")), &health); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join("shop", "pod-"+prodUID, federate.ContainersFile)
		if len(health.Ignored) != 1 || !strings.HasPrefix(health.Ignored[0], want) {
			t.Errorf("map %q: ignored %v, want %s", m, health.Ignored, want)
		}
	}
}
