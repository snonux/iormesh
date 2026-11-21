package k8s

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"iormesh/internal/k8sname"
)

// deployDir is the repo's deploy/, where `mage k8sManifests` writes.
const deployDir = "../../deploy"

// TestManifestsGolden: deploy/ is exactly what the generator writes, so
// a hand edit (or a generator change without `mage k8sManifests`) fails.
func TestManifestsGolden(t *testing.T) {
	files, err := Defaults().Files()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(deployDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(files) {
		t.Errorf("deploy/ has %d files, the generator writes %d: run mage k8sManifests", len(entries), len(files))
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(deployDir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("deploy/%s differs from the generator (%v): run mage k8sManifests\n%s", name, err, want)
		}
	}
}

// decoded is a manifest read back the way the API server reads it: as
// untyped data, so the assertions below see field names as written.
type decoded = map[string]any

func decodeFile(t *testing.T, name string) decoded {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(deployDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var d decoded
	if strings.HasSuffix(name, ".json") {
		err = json.Unmarshal(b, &d)
	} else {
		err = yaml.Unmarshal(b, &d)
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return d
}

// at walks d along keys; an int key indexes a list.
func at(t *testing.T, d any, keys ...any) any {
	t.Helper()
	for _, k := range keys {
		switch k := k.(type) {
		case string:
			m, ok := d.(map[string]any)
			if !ok {
				t.Fatalf("at %v: not a map: %v", keys, d)
			}
			d = m[k]
		case int:
			l, ok := d.([]any)
			if !ok || k >= len(l) {
				t.Fatalf("at %v: no element %d: %v", keys, k, d)
			}
			d = l[k]
		}
	}
	return d
}

func strs(t *testing.T, v any) []string {
	t.Helper()
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		out = append(out, e.(string))
	}
	return out
}

// TestPreparePatch: the pod-level fields every mode needs (decision #13)
// and no container, nor a pod-level security context (task 5h4: an
// SELinux type set there would move the app containers off container_t).
func TestPreparePatch(t *testing.T) {
	spec := at(t, decodeFile(t, "prepare-patch.yaml"), "spec", "template", "spec")
	checkPodVolumes(t, spec)
	if at(t, spec, "hostUsers") != true {
		t.Errorf("hostUsers %v, want true", at(t, spec, "hostUsers"))
	}
	if m := spec.(map[string]any); m["containers"] != nil || m["shareProcessNamespace"] != nil ||
		m["securityContext"] != nil {
		t.Errorf("prepare patch adds more than volumes and hostUsers: %v", m)
	}
}

// TestSidecarPatch: the prepare patch's fields, one PID namespace for the
// pod, the tracer container pinned by digest, and no pod-level security
// context (the SELinux type is the tracer container's alone, task 5h4).
func TestSidecarPatch(t *testing.T) {
	spec := at(t, decodeFile(t, "sidecar-patch.yaml"), "spec", "template", "spec")
	checkPodVolumes(t, spec)
	if sc := spec.(map[string]any)["securityContext"]; sc != nil {
		t.Errorf("pod-level securityContext %v, want none", sc)
	}
	if at(t, spec, "hostUsers") != true || at(t, spec, "shareProcessNamespace") != true {
		t.Errorf("hostUsers %v, shareProcessNamespace %v; want both true",
			at(t, spec, "hostUsers"), at(t, spec, "shareProcessNamespace"))
	}
	c := at(t, spec, "containers", 0)
	if at(t, c, "name") != TracerContainer || len(at(t, spec, "containers").([]any)) != 1 {
		t.Errorf("containers %v, want just %q", at(t, spec, "containers"), TracerContainer)
	}
	image, _ := at(t, c, "image").(string)
	if !k8sname.Pinned(image) || !strings.Contains(image, "@sha256:") {
		t.Errorf("image %q is not pinned by digest", image)
	}
	args := strs(t, at(t, c, "args"))
	if len(args) < 5 || args[0] != "run" ||
		!slices.Equal(args[len(args)-4:], []string{"-disk-limit", "0", "-disk-max-bytes", "1536MiB"}) {
		t.Errorf("args %q, want an iormesh run capped at 3/4 of the 2Gi emptyDir, no percent limit", args)
	}
	if at(t, c, "resources", "requests", "cpu") != "50m" || at(t, c, "resources", "requests", "memory") != "128Mi" ||
		at(t, c, "resources", "limits", "cpu") != "1" || at(t, c, "resources", "limits", "memory") != "1Gi" {
		t.Errorf("resources %v, want small requests and start's limits", at(t, c, "resources"))
	}
	checkTracerContainer(t, c)
}

// TestDebugProfile: kubectl debug --custom gets the same security
// context, mounts and env, and nothing kubectl's own flags set.
func TestDebugProfile(t *testing.T) {
	c := decodeFile(t, "debug-profile.json")
	for _, k := range []string{"name", "image", "args", "command", "resources"} {
		if _, ok := c[k]; ok {
			t.Errorf("debug profile sets %q, which kubectl debug's flags own", k)
		}
	}
	checkTracerContainer(t, c)
}

// checkPodVolumes asserts decision #13's two volumes: tracefs from the
// host and a size-bounded emptyDir.
func checkPodVolumes(t *testing.T, spec any) {
	t.Helper()
	vols := at(t, spec, "volumes").([]any)
	if len(vols) != 2 {
		t.Fatalf("volumes %v, want tracefs and the trace dir", vols)
	}
	if at(t, vols[0], "name") != TracefsVolume || at(t, vols[0], "hostPath", "path") != "/sys/kernel/tracing" ||
		at(t, vols[0], "hostPath", "type") != "Directory" {
		t.Errorf("tracefs volume %v", vols[0])
	}
	if at(t, vols[1], "name") != TracesVolume || at(t, vols[1], "emptyDir", "sizeLimit") != DefaultTraceLimit {
		t.Errorf("trace volume %v, want an emptyDir with sizeLimit %s", vols[1], DefaultTraceLimit)
	}
}

// checkTracerContainer asserts the container spec both modes share
// (docs/implementation.md M7 step 8).
func checkTracerContainer(t *testing.T, c any) {
	t.Helper()
	sc := at(t, c, "securityContext")
	// YAML decodes the number as an int, JSON as a float64.
	if uid := at(t, sc, "runAsUser"); uid != 0 && uid != float64(0) {
		t.Errorf("runAsUser %v, want 0", uid)
	}
	if at(t, sc, "allowPrivilegeEscalation") != false || at(t, sc, "privileged") != nil {
		t.Errorf("allowPrivilegeEscalation %v, privileged %v; want false and unset",
			at(t, sc, "allowPrivilegeEscalation"), at(t, sc, "privileged"))
	}
	if at(t, sc, "seccompProfile", "type") != "RuntimeDefault" {
		t.Errorf("seccomp %v, want RuntimeDefault", at(t, sc, "seccompProfile"))
	}
	// spc_t, not container_t, which is denied BPF on an SELinux-enforcing
	// node, at level s0, so its relabels never lock the app out (decision
	// #13, task 5h4).
	if se := at(t, sc, "seLinuxOptions"); at(t, se, "type") != DefaultSELinuxType ||
		at(t, se, "level") != DefaultSELinuxLevel || len(se.(map[string]any)) != 2 {
		t.Errorf("seLinuxOptions %v, want only type %s and level %s", se, DefaultSELinuxType, DefaultSELinuxLevel)
	}
	if drop := strs(t, at(t, sc, "capabilities", "drop")); !slices.Equal(drop, []string{"ALL"}) {
		t.Errorf("drop %q, want ALL", drop)
	}
	add := strs(t, at(t, sc, "capabilities", "add"))
	want := []string{"BPF", "PERFMON", "SYS_RESOURCE", "SYS_PTRACE", "DAC_OVERRIDE", "KILL", "CHECKPOINT_RESTORE"}
	if !slices.Equal(add, want) {
		t.Errorf("capabilities %q, want exactly decision #13's %q", add, want)
	}
	mounts := at(t, c, "volumeMounts").([]any)
	if len(mounts) != 2 || at(t, mounts[0], "name") != TracefsVolume ||
		at(t, mounts[0], "mountPath") != "/sys/kernel/tracing" || at(t, mounts[0], "readOnly") != true ||
		at(t, mounts[1], "name") != TracesVolume || at(t, mounts[1], "mountPath") != "/opt/iormesh/data" ||
		at(t, mounts[1], "readOnly") != nil {
		t.Errorf("volumeMounts %v, want tracefs read-only and the trace dir writable", mounts)
	}
	checkPodEnv(t, at(t, c, "env").([]any))
}

// checkPodEnv: the downward-API variables PodFromEnv reads, from the
// pod's own metadata.
func checkPodEnv(t *testing.T, env []any) {
	t.Helper()
	want := map[string]string{EnvPodName: "metadata.name", EnvPodNamespace: "metadata.namespace",
		EnvPodUID: "metadata.uid"}
	got := map[string]string{}
	for _, e := range env {
		got[at(t, e, "name").(string)], _ = at(t, e, "valueFrom", "fieldRef", "fieldPath").(string)
	}
	if len(got) != len(want) {
		t.Errorf("env %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("env %s from %q, want %q", k, got[k], v)
		}
	}
}

// TestManifestsValidate: no tag-only or malformed image, no unbounded or
// odd size, no command line that is not a run.
func TestManifestsValidate(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("ab", 32)
	good := Manifests{Image: "ghcr.io/example/iormesh" + digest, TraceLimit: "512Mi", SidecarArgs: []string{"run"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, img := range []string{"", "ghcr.io/example/iormesh:v1",
		"ghcr.io/example/iormesh:latest", "ghcr.io/Example/iormesh" + digest,
		"ghcr.io/example/iormesh@sha256:abc", "ghcr.io/example/iormesh@sha512:" + strings.Repeat("ab", 64)} {
		m := good
		m.Image = img
		if m.Validate() == nil {
			t.Errorf("image %q accepted", img)
		}
	}
	for _, limit := range []string{"", "0Gi", "2gi", "-1Gi", "2Ki", "1Mi", "127Mi", "1e100", "99999Ei", "2m",
		"1 Gi", "2Gi\n"} {
		m := good
		m.TraceLimit = limit
		if m.Validate() == nil {
			t.Errorf("trace limit %q accepted", limit)
		}
	}
	for _, limit := range []string{"128Mi", "2G", "1.5Gi", "2147483648", "2e9"} {
		m := good
		m.TraceLimit = limit
		if err := m.Validate(); err != nil {
			t.Errorf("trace limit %q refused: %v", limit, err)
		}
	}
	for _, args := range [][]string{nil, {}, {"attach"}, {"-pid", "1"}} {
		m := good
		m.SidecarArgs = args
		if m.Validate() == nil {
			t.Errorf("args %q accepted", args)
		}
	}
	if _, err := (Manifests{}).Files(); err == nil {
		t.Error("Files rendered an empty Manifests")
	}
}
