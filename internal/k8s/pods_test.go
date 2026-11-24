package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// testUID returns a valid pod UID ending in n.
func testUID(n int) string {
	return fmt.Sprintf("0123abcd-0000-4000-8000-%012d", n)
}

// podItem is one pod object as `kubectl get` prints it (the fields the
// lister reads), for a list or a single get; a tracer is an ephemeral
// container that is running.
func podItem(name, uid, phase string, tracer bool) map[string]any {
	spec := map[string]any{"containers": []map[string]any{{"name": "app"}}}
	status := map[string]any{"phase": phase}
	if tracer {
		spec["ephemeralContainers"] = []map[string]any{{"name": TracerContainer, "image": testImage}}
		status["ephemeralContainerStatuses"] = []map[string]any{{"name": TracerContainer,
			"state": map[string]any{"running": map[string]any{}}}}
	}
	return map[string]any{"metadata": map[string]any{"name": name, "uid": uid},
		"spec": spec, "status": status}
}

// TestPodStateStopped: a tracer container that terminated (OOM, crash)
// or has no status yet is reported as stopped, with why; a running one
// is not.
func TestPodStateStopped(t *testing.T) {
	f := newFake(t)
	item := podItem("web-a", testUID(1), "Running", true)
	item["status"].(map[string]any)["ephemeralContainerStatuses"] = []map[string]any{{"name": TracerContainer,
		"state": map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "exitCode": 137}}}}
	noStatus := podItem("web-b", testUID(2), "Running", true)
	delete(noStatus["status"].(map[string]any), "ephemeralContainerStatuses")
	f.pod(t, podList(t, item, noStatus, podItem("web-c", testUID(3), "Running", true)))
	pods, err := newTestLister(t).Select(context.Background(), "app=web")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"terminated (OOMKilled, exit code 137)", "without a status yet", ""}
	for i, p := range pods {
		if p.Stopped != want[i] {
			t.Errorf("%s: Stopped %q, want %q", p.Name, p.Stopped, want[i])
		}
	}
}

// TestPodStateContainers (task jk4): the lister maps every container
// status's runtime id — app, init and ephemeral, the running run's and
// the one before the last restart — to the container's name, the
// runtime scheme cut; a container not started yet (no id), an id that
// is not 64 hex digits and a name no API server issues add nothing.
func TestPodStateContainers(t *testing.T) {
	id := func(c string) string { return strings.Repeat(c, 64) }
	f := newFake(t)
	item := podItem("web-a", testUID(1), "Running", true)
	st := item["status"].(map[string]any)
	st["containerStatuses"] = []map[string]any{
		{"name": "app", "containerID": "containerd://" + id("a"),
			"lastState": map[string]any{"terminated": map[string]any{"containerID": "containerd://" + id("b")}}},
		{"name": "sidecar", "containerID": "cri-o://" + id("c")},
		{"name": "pending"},
		{"name": "short", "containerID": "containerd://abc"},
		{"name": "Bad\u001b[31m", "containerID": "containerd://" + id("d")},
	}
	st["initContainerStatuses"] = []map[string]any{{"name": "init", "containerID": "docker://" + id("e")}}
	st["ephemeralContainerStatuses"] = []map[string]any{{"name": TracerContainer,
		"containerID": "containerd://" + id("f"), "state": map[string]any{"running": map[string]any{}}}}
	f.pod(t, podList(t, item))
	pods, err := newTestLister(t).Select(context.Background(), "app=web")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{id("a"): "app", id("b"): "app", id("c"): "sidecar", id("e"): "init",
		id("f"): TracerContainer}
	if got := pods[0].Containers; !maps.Equal(got, want) {
		t.Errorf("Containers %v, want %v", got, want)
	}
}

// podList is `kubectl get pods -o json` holding items.
func podList(t *testing.T, items ...map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// testScope is the scope testTarget lives in.
func testScope() Scope {
	tg := testTarget()
	return Scope{Context: tg.Context, Namespace: tg.Namespace, Image: tg.Image, Mode: tg.Mode, Profile: tg.Profile,
		SELinuxType: &tg.SELinuxType, SELinuxLevel: &tg.SELinuxLevel}
}

// newTestLister is NewLister on testScope, failing the test on refusal.
func newTestLister(t *testing.T) *Lister {
	t.Helper()
	l, err := NewLister(testScope())
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestSelectArgvAndFilter: Select passes the selector as one
// --selector= element with the scope's context and namespace, keeps only
// running pods that are not terminating, sorts them, and says which carry
// the tracer container.
func TestSelectArgvAndFilter(t *testing.T) {
	f := newFake(t)
	// web-e is terminating, yet Running until its containers stop: the
	// old pod of a rollout, which deploy must not pick (task yb4).
	terminating := podItem("web-e", testUID(5), "Running", false)
	terminating["metadata"].(map[string]any)["deletionTimestamp"] = "2026-11-21T08:40:00Z"
	f.pod(t, podList(t, podItem("web-b", testUID(2), "Running", false),
		podItem("web-a", testUID(1), "Running", true), podItem("web-c", testUID(3), "Pending", true),
		podItem("web-d", testUID(4), "Succeeded", true), terminating))
	pods, err := newTestLister(t).Select(context.Background(), "app=web,tier!=db")
	if err != nil {
		t.Fatal(err)
	}
	none := map[string]string{} // no status carries a container id
	want := []PodState{{Name: "web-a", UID: testUID(1), Tracer: true, Container: TracerContainer, Containers: none},
		{Name: "web-b", UID: testUID(2), Containers: none}}
	if !reflect.DeepEqual(pods, want) {
		t.Errorf("pods = %+v, want %+v", pods, want)
	}
	wantArgv := []string{"--context=kind-iormesh", "--namespace=shop", "get", "pods",
		"--selector=app=web,tier!=db", "--output=json"}
	if calls := f.calls(t); len(calls) != 1 || !slices.Equal(calls[0], wantArgv) {
		t.Errorf("argv %q, want %q", calls, wantArgv)
	}
}

// TestSelectNoneOrTooMany: a selector matching no running pod is
// ErrNoPod (never an empty list), and more than MaxSelected running pods
// ErrTooManyPods.
func TestSelectNoneOrTooMany(t *testing.T) {
	f := newFake(t)
	for _, list := range []string{podList(t), podList(t, podItem("web-a", testUID(1), "Pending", false))} {
		f.pod(t, list)
		pods, err := newTestLister(t).Select(context.Background(), "app=web")
		if !errors.Is(err, ErrNoPod) || pods != nil {
			t.Errorf("list %s: pods %v, err = %v, want ErrNoPod", list, pods, err)
		}
	}
	var many []map[string]any
	for i := range MaxSelected + 1 {
		many = append(many, podItem(fmt.Sprintf("web-%d", i), testUID(i), "Running", true))
	}
	f.pod(t, podList(t, many...))
	if _, err := newTestLister(t).Select(context.Background(), "app=web"); !errors.Is(err, ErrTooManyPods) {
		t.Errorf("%d pods: err = %v, want ErrTooManyPods", len(many), err)
	}
	f.pod(t, podList(t, many[:MaxSelected]...))
	if pods, err := newTestLister(t).Select(context.Background(), "app=web"); err != nil || len(pods) != MaxSelected {
		t.Errorf("%d pods: %d, %v", MaxSelected, len(pods), err)
	}
}

// TestSelectRefusals: a selector outside the accepted form never reaches
// kubectl; a pod whose name or UID is not the API server's form (both
// become a local path or an argv) fails the lookup; kubectl's own
// failure is ErrUnreachable.
func TestSelectRefusals(t *testing.T) {
	f := newFake(t)
	for _, sel := range []string{"", "app!=web", "app", "!app", "app in (a,b)", "--all", "a=b c", "app=web,"} {
		if _, err := newTestLister(t).Select(context.Background(), sel); !errors.Is(err, ErrName) {
			t.Errorf("selector %q: err = %v, want ErrName", sel, err)
		}
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("kubectl ran: %q", calls)
	}
	for _, item := range []map[string]any{podItem("web-a", "../../etc", "Running", true),
		podItem("Web A", testUID(1), "Running", true)} {
		f.pod(t, podList(t, item))
		if _, err := newTestLister(t).Select(context.Background(), "app=web"); !errors.Is(err, ErrName) {
			t.Errorf("item %v: err = %v, want ErrName", item, err)
		}
	}
	t.Setenv("FAKE_KUBECTL_EXIT", "1")
	if _, err := newTestLister(t).Select(context.Background(), "app=web"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("kubectl failing: err = %v, want ErrUnreachable", err)
	}
}

// TestGet: a named pod that runs is returned with its UID; one that is
// pending is ErrNoPod; kubectl failing (pod gone) is ErrUnreachable.
func TestGet(t *testing.T) {
	f := newFake(t)
	b, _ := json.Marshal(podItem("checkout-0", testUID(7), "Running", true))
	f.pod(t, string(b))
	st, err := newTestLister(t).Get(context.Background(), "checkout-0")
	if err != nil || !reflect.DeepEqual(st, PodState{Name: "checkout-0", UID: testUID(7), Tracer: true,
		Container: TracerContainer, Containers: map[string]string{}}) {
		t.Fatalf("Get = %+v, %v", st, err)
	}
	want := []string{"--context=kind-iormesh", "--namespace=shop", "get", "pod", "checkout-0", "--output=json"}
	if calls := f.calls(t); !slices.Equal(calls[0], want) {
		t.Errorf("argv %q, want %q", calls[0], want)
	}
	b, _ = json.Marshal(podItem("checkout-0", testUID(7), "Pending", true))
	f.pod(t, string(b))
	if _, err := newTestLister(t).Get(context.Background(), "checkout-0"); !errors.Is(err, ErrNoPod) {
		t.Errorf("pending: err = %v, want ErrNoPod", err)
	}
	t.Setenv("FAKE_KUBECTL_EXIT", "1")
	if _, err := newTestLister(t).Get(context.Background(), "checkout-0"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("gone: err = %v, want ErrUnreachable", err)
	}
}

// TestListerSidecarTracer: in sidecar mode the tracer is a regular
// container, and an ephemeral one does not count.
func TestListerSidecarTracer(t *testing.T) {
	f := newFake(t)
	item := podItem("web-a", testUID(1), "Running", true)
	f.pod(t, podList(t, item))
	s := testScope()
	s.Mode = ModeSidecar
	l, err := NewLister(s)
	if err != nil {
		t.Fatal(err)
	}
	if pods, err := l.Select(context.Background(), "app=web"); err != nil || pods[0].Tracer {
		t.Errorf("ephemeral tracer counted in sidecar mode: %+v, %v", pods, err)
	}
	item["spec"].(map[string]any)["containers"] = []map[string]any{{"name": "app"}, {"name": TracerContainer}}
	f.pod(t, podList(t, item))
	if pods, err := l.Select(context.Background(), "app=web"); err != nil || !pods[0].Tracer {
		t.Errorf("sidecar not seen: %+v, %v", pods, err)
	}
}

// TestPodIdentity: the host id and FQDN a pod's tracer reports (7f4's
// decision) — the UID, not the name, keys both.
func TestPodIdentity(t *testing.T) {
	if got := PodHostID(testUID(1)); got != "pod-"+testUID(1) {
		t.Errorf("PodHostID = %q", got)
	}
	if got := PodFQDN("checkout-0", "shop", testUID(1)); got != "checkout-0.shop.0123abcd" {
		t.Errorf("PodFQDN = %q", got)
	}
}

// TestFetchLock: a second Fetch into a directory another holds is
// refused at once (ErrBusy) without running kubectl; once released, the
// directory can be fetched into again.
func TestFetchLock(t *testing.T) {
	f := newFake(t)
	f.tar(t, emptyTar(t))
	local := filepath.Join(t.TempDir(), "pod")
	if err := os.Mkdir(local, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockDir(local)
	if err != nil {
		t.Fatal(err)
	}
	k := newTestKubectl(t, testTarget())
	err = k.Fetch(context.Background(), TraceRoot+"/shop/ready", local)
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), local) {
		t.Fatalf("second fetch: err = %v, want ErrBusy naming %s", err, local)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("kubectl ran under a held lock: %q", calls)
	}
	unlock()
	if err := k.Fetch(context.Background(), TraceRoot+"/shop/ready", local); err != nil {
		t.Errorf("fetch after unlock: %v", err)
	}
}

// emptyTar is the stream of an empty ready/.
func emptyTar(t *testing.T) []byte {
	t.Helper()
	return hostileTar(t)
}
