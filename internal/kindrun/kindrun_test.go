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
// oi4: sha256:other, the operator's own eclipse-temurin pull); with
// every step succeeding the kubeconfig and state go.
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
	if err := c.RecordUsed(s, UsedImages(setup, c, Ran{Registry: true, Node: true, JRE: true})); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fake{replies: map[string]string{"reference=kindest/node": "sha256:old\nsha256:new\n",
		"reference=registry": "sha256:reg\n", "reference=eclipse-temurin": "sha256:jre\nsha256:other\n"}}
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

// TestTeardownFailedStepKeepsState (task qi4): a removal step that
// fails — every step but helm's uninstall exits 0 when its object is
// already gone, so a failure is a real one, such as sudo or the daemon
// refusing — makes Teardown fail and keep the state file, whose record
// of the pulled base images a rerun needs; the other steps still run.
// The rerun, with every step succeeding, removes the state file.
func TestTeardownFailedStepKeepsState(t *testing.T) {
	c := testConfig(t)
	if err := c.write(State{BaseImages: map[string][]string{"kindest/node": {}},
		Used: []string{"sha256:new"}}); err != nil {
		t.Fatal(err)
	}
	now := map[string]string{"reference=kindest/node": "sha256:new\n"}
	f := &fake{replies: now, fail: map[string]bool{"image rm": true}}
	var log strings.Builder
	err := Teardown(f, c, &log)
	if err == nil || !strings.Contains(err.Error(), "image rm sha256:new") {
		t.Errorf("failed image rm: error %v", err)
	}
	if _, serr := os.Stat(c.StateFile); serr != nil {
		t.Errorf("state file gone after a failed step: %v", serr)
	}
	if !strings.Contains(log.String(), "image rm") {
		t.Errorf("a failed step was not reported: %q", log.String())
	}
	if !slices.ContainsFunc(f.calls, func(a []string) bool { return slices.Contains(a, "prune") }) {
		t.Error("the steps after the failed one did not run")
	}
	if !teardownRemoved(t, c, now, "sha256:new") {
		t.Error("rerun did not remove the pulled image")
	}
	if _, serr := os.Stat(c.StateFile); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("state file left after a clean rerun: %v", serr)
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

// teardownRemoved runs Teardown with now's base repositories and
// reports whether it ran `docker image rm id`.
func teardownRemoved(t *testing.T, c Config, now map[string]string, id string) bool {
	t.Helper()
	f := &fake{replies: now}
	var log strings.Builder
	if err := Teardown(f, c, &log); err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(f.calls, func(a []string) bool {
		return slices.Equal(a, docker("image", "rm", id))
	})
}

// TestJREPulledByOperatorKept: the pinned JRE the operator pulls for
// other work is not the run's when no run built with it — neither
// between a kept run and a rerun that fails before its workload build,
// nor between Save and a failed step of one run (review of task oi4).
func TestJREPulledByOperatorKept(t *testing.T) {
	jre := map[string]string{"reference=eclipse-temurin": "sha256:jre\n"}
	setup := &fake{replies: map[string]string{"image inspect": "sha256:jre\n",
		"container inspect": "ERR"}}

	// A kept run that recorded nothing; the operator pulls; a rerun
	// fails before the workload build.
	c := testConfig(t)
	if err := c.Save(State{BaseImages: map[string][]string{}}); err != nil {
		t.Fatal(err)
	}
	pre, err := Snapshot(&fake{replies: jre}, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RecordUsed(pre, UsedImages(setup, c, Ran{})); err != nil {
		t.Fatal(err)
	}
	if teardownRemoved(t, c, jre, "sha256:jre") {
		t.Error("rerun: removed the operator's JRE")
	}
	// A rerun that does build uses it, but its own snapshot held it.
	if err := c.RecordUsed(pre, UsedImages(setup, c, Ran{JRE: true})); err != nil {
		t.Fatal(err)
	}
	if teardownRemoved(t, c, jre, "sha256:jre") {
		t.Error("building rerun: removed the JRE its snapshot held")
	}

	// One run: Save, the operator pulls, a step fails before the build.
	c = testConfig(t)
	pre, err = Snapshot(&fake{}, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Save(pre); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordUsed(pre, UsedImages(setup, c, Ran{Registry: true})); err != nil {
		t.Fatal(err)
	}
	if teardownRemoved(t, c, jre, "sha256:jre") {
		t.Error("failed run: removed the JRE pulled after Save")
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
// without the first state's base images changing; an ID the rerun's own
// snapshot held is added only when already recorded; without a state
// file none is recorded (it would count as absent before).
func TestRecordUsed(t *testing.T) {
	c := testConfig(t)
	if err := c.RecordUsed(State{}, []string{"sha256:a"}); err != nil {
		t.Fatal(err)
	}
	if _, known, _ := c.Load(); known {
		t.Fatal("RecordUsed wrote a state file without one")
	}
	first := State{BaseImages: map[string][]string{"registry": {"sha256:r"}}}
	if err := c.Save(first); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordUsed(first, []string{"sha256:b", "sha256:a", "sha256:r"}); err != nil {
		t.Fatal(err)
	}
	// The rerun's snapshot holds a and b (the first run's) and the
	// operator's d; it uses a, c and d.
	rerun := State{BaseImages: map[string][]string{"registry": {"sha256:r", "sha256:a", "sha256:b"},
		"eclipse-temurin": {"sha256:d"}}}
	if err := c.RecordUsed(rerun, []string{"sha256:a", "sha256:c", "sha256:d"}); err != nil {
		t.Fatal(err)
	}
	s, _, err := c.Load()
	if err != nil || !slices.Equal(s.Used, []string{"sha256:a", "sha256:b", "sha256:c"}) ||
		!slices.Equal(s.BaseImages["registry"], []string{"sha256:r"}) {
		t.Errorf("state %+v, %v", s, err)
	}
}

// TestUsedImagesRan: only the steps that ran this time are resolved — the
// registry container it created, the node of the cluster it created, the
// JRE of the build it ran — and an unresolvable one is left out.
func TestUsedImagesRan(t *testing.T) {
	c := testConfig(t)
	replies := map[string]string{"image inspect": "ERR", "{{.Image}} iormesh-registry": "sha256:reg\n",
		"iormesh-m7-control-plane": "sha256:node\n"}
	f := &fake{replies: replies}
	if got := UsedImages(f, c, Ran{}); got != nil || len(f.calls) != 0 {
		t.Errorf("nothing ran: %q after %q", got, f.calls)
	}
	f = &fake{replies: replies}
	if got := UsedImages(f, c, Ran{Registry: true, Node: true, JRE: true}); !slices.Equal(got,
		[]string{"sha256:reg", "sha256:node"}) || len(f.calls) != 3 {
		t.Errorf("all ran: %q after %q", got, f.calls)
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
// one gets the named data volume. It reports that it ran `docker run`
// only when it did (the registry image is then the run's to record).
func TestRegistryUp(t *testing.T) {
	c := testConfig(t)
	stopped := &fake{replies: map[string]string{"container inspect": "false\n"}}
	ran, err := RegistryUp(stopped, c)
	if err != nil || !ran {
		t.Fatalf("stopped registry: ran %v, %v", ran, err)
	}
	if len(stopped.calls) != 3 || !slices.Contains(stopped.calls[1], "rm") ||
		!slices.Contains(stopped.calls[2], "--volume=iormesh-registry-data:/var/lib/registry") {
		t.Errorf("stopped registry: %q", stopped.calls)
	}
	running := &fake{replies: map[string]string{"container inspect": "true\n"}}
	if ran, err := RegistryUp(running, c); err != nil || ran || len(running.calls) != 1 {
		t.Errorf("running registry: %q, ran %v, %v", running.calls, ran, err)
	}
	absent := &fake{replies: map[string]string{"container inspect": "ERR"}}
	if ran, err := RegistryUp(absent, c); err != nil || !ran || len(absent.calls) != 2 ||
		!slices.Contains(absent.calls[1], "run") {
		t.Errorf("no registry: %q, ran %v, %v", absent.calls, ran, err)
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
