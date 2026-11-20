package k8s

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

const uid2 = "9d8e7f60-1a2b-4c3d-8e9f-0a1b2c3d4e5f"

// TestPodHostIdentity: host_id is "pod-<uid>", the FQDN
// <name>.<namespace>.<uid[:8]> — exactly what the controller pins for the
// same pod's API metadata (Lister.state's checks, PodHostID, PodFQDN).
// A static pod's 32-hex downward-API UID is refused: the controller pins
// the mirror pod's dashed metadata.uid, so the two would disagree.
func TestPodHostIdentity(t *testing.T) {
	p := PodRef{"checkout-7d9f", "prod", uid}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.HostID() != "pod-"+uid || p.FQDN() != "checkout-7d9f.prod.3b1c9a52" {
		t.Errorf("host id %q, FQDN %q", p.HostID(), p.FQDN())
	}
	pin := federate.HostIdentity{HostID: PodHostID(uid), FQDN: PodFQDN("checkout-7d9f", "prod", uid)}
	if (federate.HostIdentity{HostID: p.HostID(), FQDN: p.FQDN()}) != pin {
		t.Errorf("tracer identity differs from the controller's pin %+v", pin)
	}
	static := PodRef{"etcd-node1", "kube-system", "0f1e2d3c4b5a69788796a5b4c3d2e1f0"}
	if err := static.Validate(); !errors.Is(err, ErrPodEnv) {
		t.Errorf("static pod: %v, want ErrPodEnv", err)
	}
}

// TestPodRefValidate: what the downward API cannot hand out is refused.
func TestPodRefValidate(t *testing.T) {
	long := strings.Repeat("a", 63)
	good := []PodRef{
		{"a", "b", uid},
		{"web-0.shard", "prod", uid},
		{strings.Repeat("a", 64), "b", uid}, // a pod name part has no 63-byte cap
		// One part of MaxFQDN-73 bytes: name.<63>.<8> = MaxFQDN.
		{strings.Repeat("a", MaxFQDN-73), long, uid},
	}
	for _, p := range good {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	bad := map[string]PodRef{
		"no name":       {"", "prod", uid},
		"no namespace":  {"a", "", uid},
		"no uid":        {"a", "prod", ""},
		"short uid":     {"a", "prod", "3b1c9a52"},
		"upper uid":     {"a", "prod", strings.ToUpper(uid)},
		"path uid":      {"a", "prod", "../../../etc/passwd"},
		"uid newline":   {"a", "prod", uid + "\n"},
		"name slash":    {"a/b", "prod", uid},
		"name dash":     {"a-", "prod", uid},
		"name dots":     {"a..b", "prod", uid},
		"name dash dot": {"a-.b", "prod", uid},
		"name upper":    {"A", "prod", uid},
		"ns dot":        {"a", "pr.od", uid},
		"ns long":       {"a", long + "a", uid},
		"fqdn too long": {strings.Repeat("a", MaxFQDN-72), long, uid},
		"name too long": {strings.Repeat(long+".", 4) + "a", "b", uid},
	}
	for name, p := range bad {
		if err := p.Validate(); !errors.Is(err, ErrPodEnv) {
			t.Errorf("%s: %v, want ErrPodEnv", name, err)
		}
	}
}

// TestPodFromEnv: all three variables set is a pod, none is no pod, and
// one or two set — or a malformed value — is ErrPodEnv, never "no pod".
func TestPodFromEnv(t *testing.T) {
	env := map[string]string{EnvPodName: "checkout-7d9f", EnvPodNamespace: "prod", EnvPodUID: uid}
	pod, ok, err := PodFromEnv(func(k string) string { return env[k] })
	if err != nil || !ok || pod != (PodRef{"checkout-7d9f", "prod", uid}) {
		t.Fatalf("%+v, %v, %v", pod, ok, err)
	}
	if _, ok, err := PodFromEnv(func(string) string { return "" }); ok || err != nil {
		t.Errorf("no variable: %v, %v; want no pod", ok, err)
	}
	for _, k := range []string{EnvPodName, EnvPodNamespace, EnvPodUID} {
		partial := map[string]string{k: env[k]}
		if _, ok, err := PodFromEnv(func(k string) string { return partial[k] }); ok || !errors.Is(err, ErrPodEnv) {
			t.Errorf("only %s: %v, %v; want ErrPodEnv", k, ok, err)
		}
		missing := maps2(env, k)
		if _, ok, err := PodFromEnv(func(k string) string { return missing[k] }); ok || !errors.Is(err, ErrPodEnv) {
			t.Errorf("without %s: %v, %v; want ErrPodEnv", k, ok, err)
		}
	}
	forged := maps2(env, "")
	forged[EnvPodUID] = "pod-x/../../y"
	if _, ok, err := PodFromEnv(func(k string) string { return forged[k] }); ok || !errors.Is(err, ErrPodEnv) {
		t.Errorf("forged uid: %v, %v; want ErrPodEnv", ok, err)
	}
	// The refusal says how to get out of it: which variables to unset.
	_, _, err = PodFromEnv(func(k string) string { return map[string]string{EnvPodUID: uid}[k] })
	for _, want := range []string{"unset all three", EnvPodName, EnvPodNamespace, EnvPodUID} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("partial env: %v does not say %q", err, want)
		}
	}
}

// TestSegmentNameFitsUnpack: the controller takes a 249-byte segment
// name, whose unpack temporary "." + name + ".part" is 255 bytes, and
// refuses a 250-byte one as no segment name (ErrStream), never failing
// on the temporary half-way through a fetch.
func TestSegmentNameFitsUnpack(t *testing.T) {
	name := func(n int) string { return strings.Repeat("a", n-len(".parquet")) + ".parquet" }
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name(maxSegmentName), Mode: 0o600, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if taken, err := Unpack(&buf, t.TempDir()); err != nil || len(taken) != 1 {
		t.Errorf("%d-byte name: %q, %v", maxSegmentName, taken, err)
	}
	buf.Reset()
	tw = tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name(maxSegmentName + 1), Mode: 0o600, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Unpack(&buf, t.TempDir()); !errors.Is(err, ErrStream) {
		t.Errorf("%d-byte name: %v, want ErrStream", maxSegmentName+1, err)
	}
}

// maps2 is a copy of m without key drop.
func maps2(m map[string]string, drop string) map[string]string {
	c := map[string]string{}
	for k, v := range m {
		if k != drop {
			c[k] = v
		}
	}
	return c
}

// TestPodSegmentsEndToEnd: two pods on one node (one boot id), each
// traced by its own tracer whose app is pid 7 in its PID namespace with
// the same incarnation, write segments named by their pod FQDNs. Every
// file name — at MaxFQDN too — passes the controller's segment-name
// check and the segment parser, the store loads them with each pod
// directory pinned to its pod identity, and the merged graph has two
// process nodes, not one.
func TestPodSegmentsEndToEnd(t *testing.T) {
	long := strings.Repeat("a", 63)
	// An FQDN of exactly MaxFQDN: <name>.<63>.<8>, the name one part of
	// more than 63 bytes (Kubernetes caps only the whole name).
	b := PodRef{strings.Repeat("b", MaxFQDN-73), long, uid2}
	if n := len(b.FQDN()); n != MaxFQDN {
		t.Fatalf("FQDN of %d bytes, want %d", n, MaxFQDN)
	}
	// Store directory → pod, and the identity the controller pins there
	// from the API metadata (internal/ctl: the directory is PodHostID,
	// the pin PodHostID and PodFQDN): a tracer identity that differed
	// would make its sessions bad.
	pods, known := map[string]PodRef{}, map[string]federate.HostIdentity{}
	for _, p := range []PodRef{{"checkout-7d9f", "prod", uid}, b} {
		dir := PodHostID(p.UID)
		pods[dir] = p
		known[dir] = federate.HostIdentity{HostID: PodHostID(p.UID), FQDN: PodFQDN(p.Name, p.Namespace, p.UID)}
	}
	store := t.TempDir()
	for dir, p := range pods {
		writePodSegments(t, filepath.Join(store, "shop", dir), p)
	}
	segs, err := federate.LoadSegments(context.Background(), store, nil, federate.LoadOptions{Known: known})
	if err != nil {
		t.Fatal(err)
	}
	if len(segs.Bad) != 0 || len(segs.Ignored) != 0 || len(segs.Sources) != 2 {
		t.Fatalf("sources %d, bad %v, ignored %v", len(segs.Sources), segs.Bad, segs.Ignored)
	}
	for dir, p := range pods {
		if got := segs.Hosts[dir]; got != (federate.HostIdentity{HostID: p.HostID(), FQDN: p.FQDN()}) {
			t.Errorf("dir %s: identity %+v", dir, got)
		}
	}
	srcs := make([]federate.Source, len(segs.Sources))
	for i, s := range segs.Sources {
		srcs[i] = s
	}
	r, err := federate.Merge(context.Background(), srcs, federate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var procs []string
	for _, n := range r.Graph.Nodes {
		if n.Type == schema.NodeProcess {
			procs = append(procs, n.ID)
		}
	}
	slices.Sort(procs)
	want := []string{schema.ProcessNodeID("pod-"+uid, 7, 42), schema.ProcessNodeID("pod-"+uid2, 7, 42)}
	if !slices.Equal(procs, want) {
		t.Errorf("process nodes %q, want %q", procs, want)
	}
}

// writePodSegments writes one event of pid 7 (incarnation 42) as a tracer
// in pod p would, and moves the published files into dst as collect
// does, checking each name as the controller's fetch does.
func writePodSegments(t *testing.T, dst string, p PodRef) {
	t.Helper()
	srv := filepath.Join(t.TempDir(), "shop")
	t0 := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	w, err := segment.New(segment.Config{Dir: srv, FQDN: p.FQDN(), Session: "0123abcd", Project: "shop",
		Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	ev := schema.Event{SchemaVersion: schema.Version, Project: "shop", Seq: 1, BootNS: 1,
		WallNS: uint64(t0.UnixNano()), HostID: p.HostID(), PID: 7, PIDInc: 42, TID: 7, Comm: "java",
		Event: schema.KindOpenat, FD: 3, Args: schema.Args{Path: ptrTo("/data/x")},
		Attribution: schema.AttributionNone}
	if err := w.Event(&ev); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(srv, segment.ReadyDir)
	files := map[string][]byte{}
	ents, err := os.ReadDir(ready)
	if err != nil || len(ents) == 0 {
		t.Fatalf("ready: %v, %v", ents, err)
	}
	for _, en := range ents {
		b, err := os.ReadFile(filepath.Join(ready, en.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[en.Name()] = b
	}
	// The real files go into dst through the controller's own unpack
	// (temporaries and all), as collect fetches them.
	unpackFiles(t, dst, p, files)
	// The longest names a session could publish — table files with a
	// seq far past six digits — unpack too, into a scratch dir.
	long := map[string][]byte{}
	for _, seq := range []int{1_000_000, 1_000_000_000} {
		for _, table := range []string{"classes", "methods", "threads", "stacks"} {
			long[segment.TableName(p.FQDN(), "0123abcd", t0, seq, table)] = nil
		}
	}
	unpackFiles(t, t.TempDir(), p, long)
}

// unpackFiles streams files as the tracer's `iormesh ready` would (a
// tar) through Unpack into dst, checking that every name passes the
// segment parser with p's FQDN and that Unpack takes them all.
func unpackFiles(t *testing.T, dst string, p PodRef, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, b := range files {
		if f, err := segment.ParseName(name); err != nil || f.FQDN != p.FQDN() || len(name) > maxSegmentName {
			t.Errorf("%s (%d bytes): %+v, %v", name, len(name), f, err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)),
			Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	taken, err := Unpack(&buf, dst)
	if err != nil || len(taken) != len(files) {
		t.Fatalf("unpack: took %d of %d: %v", len(taken), len(files), err)
	}
}

func ptrTo[T any](v T) *T { return &v }
