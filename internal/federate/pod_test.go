package federate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

// Two pods of the namespace filter's tests (task sh4).
const (
	prodUID = "3b1c9a52-7e0d-4c1f-9a8e-2f6d5b4c3a21"
	devUID  = "9d8c7b6a-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
)

// podTrace is an in-pod tracer's trace of pod name in namespace ns: a
// client connection whose events carry the pod object ns/name (podNS
// lets a test forge another), stored under <project>/pod-<uid>/ with
// the pod FQDN the tracer gives its segments.
func podTrace(t *testing.T, store, uid, name, ns, podNS string) {
	t.Helper()
	tr := newTrace("pod-"+uid, "shop")
	tr.client(100, "c-"+uid[:4], "10.0.2.2:40000", "10.0.9.9:80", ISNPair{1, 2}, wall0)
	for i := range tr.events {
		tr.events[i].Pod = &schema.Pod{Namespace: podNS, Name: name,
			ContainerID: strings.Repeat("ab", 32)}
	}
	srv := filepath.Join(t.TempDir(), tr.project)
	w, err := segment.New(segment.Config{Dir: srv, FQDN: name + "." + ns + "." + uid[:8],
		Session: "a1b2c3d4", Project: tr.project, Now: func() time.Time { return segT0 }})
	if err != nil {
		t.Fatal(err)
	}
	for i := range tr.events {
		if err := w.Event(&tr.events[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	moveReady(t, filepath.Join(srv, segment.ReadyDir), filepath.Join(store, tr.project, "pod-"+uid))
}

// TestInNamespace: a store with a prod pod, a dev pod and a machine host
// filtered to prod keeps the prod pod's sessions alone — by the pinned
// pod FQDN, so the machine host (no namespace) goes too — and each
// segment says its namespace.
func TestInNamespace(t *testing.T) {
	store := t.TempDir()
	podTrace(t, store, prodUID, "web-7d9f", "prod", "prod")
	podTrace(t, store, devUID, "web-1a2b", "dev", "dev")
	writeSegments(t, store, "app1", "a1b2c3d4", meshTraces()["a"], 100)
	segs, err := LoadSegments(context.Background(), store, nil, LoadOptions{})
	if err != nil || len(segs.Bad) != 0 {
		t.Fatalf("load: %v, bad %v", err, segs.Bad)
	}
	got := segs.InNamespace("prod")
	if len(got.Sources) != 1 || got.Sources[0].Host != "pod-"+prodUID || got.Sources[0].Namespace() != "prod" {
		t.Fatalf("prod sources %v", sourceNames(got))
	}
	for _, tf := range got.TableFiles {
		if tf.Host != "pod-"+prodUID {
			t.Errorf("table file %s of another host kept", tf.Name())
		}
	}
	if n := len(segs.InNamespace("staging").Sources); n != 0 {
		t.Errorf("staging: %d sources, want none", n)
	}
	for _, s := range segs.Sources {
		want := map[string]string{"pod-" + prodUID: "prod", "pod-" + devUID: "dev"}[s.Host]
		if s.Namespace() != want {
			t.Errorf("%s: namespace %q, want %q", s.Name(), s.Namespace(), want)
		}
	}
}

// TestPodObjectMustMatchFQDN: a pod session whose events claim another
// namespace than its pinned FQDN names is left out whole, so a tracer
// cannot slip into another namespace's filtered build.
func TestPodObjectMustMatchFQDN(t *testing.T) {
	store := t.TempDir()
	podTrace(t, store, prodUID, "web-7d9f", "prod", "kube-system")
	segs, err := LoadSegments(context.Background(), store, nil, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(segs.Sources) != 0 || len(segs.Bad) == 0 ||
		!strings.Contains(segs.Bad[0].Err.Error(), "kube-system") {
		t.Fatalf("sources %v, bad %v: want the forged session left out", sourceNames(segs), segs.Bad)
	}
}

// sourceNames lists segs' sources by name.
func sourceNames(segs Segments) []string {
	var out []string
	for _, s := range segs.Sources {
		out = append(out, s.Name())
	}
	return out
}
