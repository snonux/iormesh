package kindrun

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fake records every argv and answers Output from canned replies keyed
// by a substring of the joined argv; fail makes matching Runs fail.
type fake struct {
	calls   [][]string
	replies map[string]string
	fail    map[string]bool
}

func (f *fake) Run(argv ...string) error {
	f.calls = append(f.calls, argv)
	for k := range f.fail {
		if strings.Contains(strings.Join(argv, " "), k) {
			return errors.New("failed")
		}
	}
	return nil
}

func (f *fake) Output(argv ...string) (string, error) {
	f.calls = append(f.calls, argv)
	line := strings.Join(argv, " ")
	for k, v := range f.replies {
		if strings.Contains(line, k) {
			if v == "ERR" {
				return "", errors.New("no such object")
			}
			return v, nil
		}
	}
	return "", nil
}

func testConfig(t *testing.T) Config {
	dir := t.TempDir()
	return Default(filepath.Join(dir, "kubeconfig"), filepath.Join(dir, "state.json"))
}

// TestTeardownScoped: teardown names only the run's own objects — the
// cluster, the registry with its volumes, the run's tags, its labelled
// images and the base images its setup used that were absent before —
// never a system prune or an unfiltered prune, nor an image that
// appeared in a base repository since without the run using it (task
// oi4: sha256:other, the operator's own eclipse-temurin pull); the
// kubeconfig and state go.
func TestTeardownScoped(t *testing.T) {
	c := testConfig(t)
	before := &fake{replies: map[string]string{"reference=kindest/node": "sha256:old\n", "reference=registry": ""}}
	s, err := Snapshot(before, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Save(s); err != nil {
		t.Fatal(err)
	}
	setup := &fake{replies: map[string]string{"{{.Image}} iormesh-registry": "sha256:reg\n",
		"image inspect": "sha256:jre\n", "iormesh-m7-control-plane": "sha256:new\n"}}
	if err := c.RecordUsed(UsedImages(setup, c)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fake{replies: map[string]string{"reference=kindest/node": "sha256:old\nsha256:new\n",
		"reference=registry": "sha256:reg\n", "reference=eclipse-temurin": "sha256:jre\nsha256:other\n"},
		fail: map[string]bool{"volume rm": true}}
	var log strings.Builder
	if err := Teardown(f, c, &log); err != nil {
		t.Fatal(err)
	}
	var runs []string
	for _, a := range f.calls {
		line := strings.Join(a, " ")
		if strings.Contains(line, "system") || strings.Contains(line, "prune") &&
			!strings.Contains(line, "--filter=label=org.iormesh.acceptance=m7") {
			t.Errorf("unscoped removal: %s", line)
		}
		if strings.Contains(line, "sha256:other") {
			t.Errorf("removed an image the run did not use: %s", line)
		}
		if !strings.Contains(line, "image ls") {
			runs = append(runs, line)
		}
	}
	want := []string{
		"sudo -n kind delete cluster --name=iormesh-m7",
		"sudo -n docker rm --force --volumes iormesh-registry",
		"sudo -n docker volume rm --force iormesh-registry-data",
		"sudo -n docker image rm --force localhost:5001/iormesh/tracer:m7",
		"sudo -n docker image rm --force localhost:5001/iormesh/workload:m7",
		"sudo -n docker image prune --all --force --filter=label=org.iormesh.acceptance=m7",
		"sudo -n docker image rm sha256:jre",
		"sudo -n docker image rm sha256:new",
		"sudo -n docker image rm sha256:reg",
	}
	if !slices.Equal(runs, want) {
		t.Errorf("teardown ran\n%s\nwant\n%s", strings.Join(runs, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(log.String(), "volume rm") {
		t.Errorf("a failed step was not reported: %q", log.String())
	}
	for _, p := range []string{c.Kubeconfig, c.StateFile} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind", p)
		}
	}
}

// TestTeardownWithoutState: with no record of the base images (a
// teardown of a run that never saved one) no base image is removed.
func TestTeardownWithoutState(t *testing.T) {
	c := testConfig(t)
	f := &fake{replies: map[string]string{"reference=kindest/node": "sha256:x\n"}}
	var log strings.Builder
	if err := Teardown(f, c, &log); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.calls {
		if slices.Contains(a, "sha256:x") {
			t.Errorf("removed a base image without state: %q", a)
		}
	}
	if !strings.Contains(log.String(), "none removed") {
		t.Errorf("no notice: %q", log.String())
	}
}

// TestSaveKeepsFirstState: a rerun over a kept cluster keeps the state
// of the first run.
func TestSaveKeepsFirstState(t *testing.T) {
	c := testConfig(t)
	if err := c.Save(State{BaseImages: map[string][]string{"registry": {"a"}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(State{BaseImages: map[string][]string{"registry": {"a", "b"}}}); err != nil {
		t.Fatal(err)
	}
	s, known, err := c.Load()
	if err != nil || !known || len(s.BaseImages["registry"]) != 1 {
		t.Errorf("state %+v, %v, %v", s, known, err)
	}
}

// TestPulledOnlyUsed: of the used IDs, one a base repository held
// before the run stays, one gone since is not named, and a new ID the
// run did not use is never named (task oi4).
func TestPulledOnlyUsed(t *testing.T) {
	before := State{BaseImages: map[string][]string{"registry": {"sha256:had"}},
		Used: []string{"sha256:had", "sha256:gone", "sha256:mine"}}
	now := State{BaseImages: map[string][]string{"registry": {"sha256:had", "sha256:mine"},
		"eclipse-temurin": {"sha256:theirs"}}}
	if got := Pulled(before, true, now); !slices.Equal(got, []string{"sha256:mine"}) {
		t.Errorf("pulled %q, want only sha256:mine", got)
	}
	if got := Pulled(State{BaseImages: now.BaseImages}, true, now); got != nil {
		t.Errorf("no used record: pulled %q", got)
	}
}

// TestRecordUsed: used IDs accumulate over reruns of a kept cluster,
// without the first state's base images changing; without a state file
// none is recorded (it would count as absent before). An ID that cannot
// be resolved is left out, and an existing cluster has no kind node.
func TestRecordUsed(t *testing.T) {
	c := testConfig(t)
	if err := c.RecordUsed([]string{"sha256:a"}); err != nil {
		t.Fatal(err)
	}
	if _, known, _ := c.Load(); known {
		t.Fatal("RecordUsed wrote a state file without one")
	}
	if err := c.Save(State{BaseImages: map[string][]string{"registry": {"sha256:r"}}}); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{{"sha256:b", "sha256:a"}, {"sha256:a", "sha256:c"}} {
		if err := c.RecordUsed(ids); err != nil {
			t.Fatal(err)
		}
	}
	s, _, err := c.Load()
	if err != nil || !slices.Equal(s.Used, []string{"sha256:a", "sha256:b", "sha256:c"}) ||
		!slices.Equal(s.BaseImages["registry"], []string{"sha256:r"}) {
		t.Errorf("state %+v, %v", s, err)
	}
	e := Existing(c.Kubeconfig, "ctx", c.StateFile)
	f := &fake{replies: map[string]string{"image inspect": "ERR", "container inspect": "sha256:reg\n"}}
	if got := UsedImages(f, e); !slices.Equal(got, []string{"sha256:reg"}) || len(f.calls) != 2 {
		t.Errorf("existing cluster used %q after %q", got, f.calls)
	}
}

// TestJREImagePinned: c.JREImage is the base image the workload's
// Containerfile pins, so UsedImages resolves the one the build pulled.
func TestJREImagePinned(t *testing.T) {
	data, err := os.ReadFile("../../scripts/container/kind-workload.Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	c := Default("k", "s")
	if !strings.Contains(string(data), "\nFROM "+c.JREImage+"\n") {
		t.Errorf("kind-workload.Containerfile does not pin FROM %s", c.JREImage)
	}
}

// TestRegistryUp: a stopped registry of the same name is removed (with
// its anonymous volumes) before the run; a running one is kept; the new
// one gets the named data volume.
func TestRegistryUp(t *testing.T) {
	c := testConfig(t)
	stopped := &fake{replies: map[string]string{"container inspect": "false\n"}}
	if err := RegistryUp(stopped, c); err != nil {
		t.Fatal(err)
	}
	if len(stopped.calls) != 3 || !slices.Contains(stopped.calls[1], "rm") ||
		!slices.Contains(stopped.calls[2], "--volume=iormesh-registry-data:/var/lib/registry") {
		t.Errorf("stopped registry: %q", stopped.calls)
	}
	running := &fake{replies: map[string]string{"container inspect": "true\n"}}
	if err := RegistryUp(running, c); err != nil || len(running.calls) != 1 {
		t.Errorf("running registry: %q, %v", running.calls, err)
	}
	absent := &fake{replies: map[string]string{"container inspect": "ERR"}}
	if err := RegistryUp(absent, c); err != nil || len(absent.calls) != 2 || !slices.Contains(absent.calls[1], "run") {
		t.Errorf("no registry: %q, %v", absent.calls, err)
	}
}

// TestKubeconfigAsUser: the cluster is created without --kubeconfig and
// the kubeconfig is written by the caller, 0600, replacing a symlink
// rather than writing through it.
func TestKubeconfigAsUser(t *testing.T) {
	c := testConfig(t)
	f := &fake{replies: map[string]string{"get kubeconfig": "apiVersion: v1\nkind: Config\n"}}
	if err := CreateCluster(f, c, "/tmp/cfg"); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.calls[0] {
		if strings.HasPrefix(a, "--kubeconfig") {
			t.Errorf("kind create got %s", a)
		}
	}
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.Symlink(target, c.Kubeconfig); err != nil {
		t.Fatal(err)
	}
	if err := WriteKubeconfig(f, c); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(c.Kubeconfig)
	if err != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0o600 {
		t.Errorf("kubeconfig %v, %v", st.Mode(), err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Error("wrote through the symlink")
	}
}

// TestDiskGuard checks Docker's data root, refuses a full one or tight
// btrfs metadata, and says when btrfs could not be checked.
func TestDiskGuard(t *testing.T) {
	base := map[string]string{"DockerRootDir": "/var/lib/docker\n", "df -B1G": "Avail\n  35\n",
		"filesystem df":    "Metadata, DUP: total=17.00GiB, used=16.02GiB\n",
		"filesystem usage": "    Device unallocated:\t\t  16.00GiB\n"}
	f := &fake{replies: base}
	if notice, err := DiskGuard(f); err != nil || notice != "" {
		t.Errorf("healthy: %q, %v", notice, err)
	}
	if !slices.Contains(f.calls[1], "/var/lib/docker") {
		t.Errorf("df ran on %q, not the data root", f.calls[1])
	}
	cases := map[string]map[string]string{
		"full": {"df -B1G": "Avail\n  12\n"},
		"btrfs": {"filesystem usage": "    Device unallocated:\t\t  1.00GiB\n",
			"filesystem df": "Metadata, DUP: total=17.00GiB, used=16.80GiB\n"},
	}
	for name, over := range cases {
		r := map[string]string{}
		for k, v := range base {
			r[k] = v
		}
		for k, v := range over {
			r[k] = v
		}
		if _, err := DiskGuard(&fake{replies: r}); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
	r := map[string]string{"DockerRootDir": "/d\n", "df -B1G": "Avail\n 50\n", "filesystem df": "ERR"}
	if notice, err := DiskGuard(&fake{replies: r}); err != nil || !strings.Contains(notice, "not checked") {
		t.Errorf("no btrfs: %q, %v", notice, err)
	}
}
