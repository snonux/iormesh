package kind

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// scenariosDir holds the workload's goldens.
const scenariosDir = "../../workload/scenarios"

// hostTrace is one pod's collected trace: its sources (segments) and its
// events in the order its tracer wrote them.
type hostTrace struct {
	sources []federate.Source
	events  []schema.Event
}

// loadStore reads the project's collected segments as the build does
// and groups them by store directory (pod-<uid>).
func loadStore(t *testing.T, store, project string) map[string]*hostTrace {
	t.Helper()
	segs, err := federate.LoadSegments(context.Background(), store, []string{project}, federate.LoadOptions{})
	if err != nil {
		t.Fatalf("load segments: %v", err)
	}
	for _, b := range segs.Bad {
		t.Errorf("bad segment %s: %v", b.Name, b.Err)
	}
	hosts := map[string]*hostTrace{}
	for _, s := range segs.Sources {
		h := hosts[s.Host]
		if h == nil {
			h = &hostTrace{}
			hosts[s.Host] = h
		}
		h.sources = append(h.sources, s)
		if err := s.Events(context.Background(), func(e *schema.Event) error {
			h.events = append(h.events, *e)
			return nil
		}); err != nil {
			t.Fatalf("%s: %v", s.Name(), err)
		}
	}
	return hosts
}

// session returns the host's session header (one session per pod: one
// tracer per pod, PLAN.md M7).
func (h *hostTrace) session(t *testing.T) schema.Session {
	t.Helper()
	var out []schema.Session
	for _, e := range h.events {
		if e.Event == schema.KindSession {
			out = append(out, *e.Session)
		}
	}
	if len(out) != 1 {
		t.Fatalf("%d session headers, want 1", len(out))
	}
	return out[0]
}

// merge merges hosts as `iormeshctl build` does and returns the result
// with the merged trace: the hosts' events interleaved by wall clock, each
// host's kept in its own order, conn ids renamed to the merged graph's
// (as integrationtests' mergeHosts).
func merge(t *testing.T, hosts ...*hostTrace) (federate.Result, normalize.Trace) {
	t.Helper()
	var sources []federate.Source
	streams := make([][]schema.Event, len(hosts))
	for i, h := range hosts {
		sources = append(sources, h.sources...)
		streams[i] = h.events
	}
	res, err := federate.Merge(context.Background(), sources, federate.Options{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	events := mergeByWall(streams)
	for i, e := range events {
		if to, ok := res.Renames[federate.LocalConn{Host: e.HostID, ConnID: e.ConnID}]; ok {
			events[i].ConnID = to
		}
	}
	return res, normalize.Trace{Events: events, Graph: res.Graph}
}

// mergeByWall interleaves the streams by wall_ns, always taking the
// earliest head, so each stream keeps its order (ties to the first).
func mergeByWall(streams [][]schema.Event) []schema.Event {
	var out []schema.Event
	next := make([]int, len(streams))
	for {
		pick := -1
		for i, s := range streams {
			if next[i] < len(s) && (pick < 0 || s[next[i]].WallNS < streams[pick][next[pick]].WallNS) {
				pick = i
			}
		}
		if pick < 0 {
			return out
		}
		out = append(out, streams[pick][next[pick]])
		next[pick]++
	}
}

// golden loads a scenario's golden with the rig's addresses replaced by
// the cluster's (old → new pairs), so the declared 4-tuples are the pods'
// and the Service's.
func golden(t *testing.T, scenario, file string, addrs ...string) normalize.Expectation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(scenariosDir, scenario, file))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer(addrs...).Replace(string(data))
	x, err := normalize.ParseExpectation([]byte(text))
	if err != nil {
		t.Fatalf("%s/%s: %v", scenario, file, err)
	}
	return x
}

// assertGolden normalizes trace to the scenario's bracket and checks it
// against want.
func assertGolden(t *testing.T, trace normalize.Trace, scenario, tmp string, want normalize.Expectation) normalize.Trace {
	t.Helper()
	got, err := normalize.Normalize(trace, normalize.Options{Bracket: scenario, TmpPrefix: tmp})
	if err != nil {
		t.Fatalf("normalize %s: %v", scenario, err)
	}
	for _, v := range normalize.Check(got, want) {
		t.Errorf("golden %s: %v", want.Scenario, v)
	}
	return got
}

// assertCaught: each mutation of want must fail the check, else the
// golden proved nothing.
func assertCaught(t *testing.T, got normalize.Trace, want normalize.Expectation,
	mutations map[string]func(*normalize.Expectation)) {
	t.Helper()
	for name, mutate := range mutations {
		wrong := want
		wrong.Events = slices.Clone(want.Events)
		wrong.Edges = slices.Clone(want.Edges)
		mutate(&wrong)
		if len(normalize.Check(got, wrong)) == 0 {
			t.Errorf("%s: the golden check passed an expectation that does not hold", name)
		}
	}
}

// assertIdentity: every pod is its own host, pod-<uid>, with the pod's
// FQDN, all on one node (one boot_id); a pod-local pid seen in two pods
// (pids are numbered per pod since task 4f4) still names two processes,
// since the process id carries the host id.
func assertIdentity(t *testing.T, hosts map[string]*hostTrace, ns string, pods map[string]podJSON) {
	t.Helper()
	boots := map[string]bool{}
	pidPods := map[uint32]map[string]bool{}
	procs := map[string]bool{}
	for dir, h := range hosts {
		boots[h.session(t).BootID] = true
		for _, e := range h.events {
			if e.HostID != dir {
				t.Fatalf("%s holds an event of host %s", dir, e.HostID)
			}
			if e.PID == 0 {
				continue
			}
			procs[fmt.Sprintf("proc:%s/%d@%d", e.HostID, e.PID, e.PIDInc)] = true
			if pidPods[e.PID] == nil {
				pidPods[e.PID] = map[string]bool{}
			}
			pidPods[e.PID][dir] = true
		}
	}
	for name, p := range pods {
		h := hosts["pod-"+p.Metadata.UID]
		if h == nil {
			t.Errorf("pod %s: no store directory pod-%s", name, p.Metadata.UID)
			continue
		}
		want := p.Metadata.Name + "." + ns + "." + p.Metadata.UID[:8]
		if fqdn := h.session(t).FQDN; fqdn != want {
			t.Errorf("pod %s: session FQDN %s, want %s", name, fqdn, want)
		}
	}
	if len(boots) != 1 {
		t.Errorf("boot ids %v: the pods share one node, want one", boots)
	}
	for pid, dirs := range pidPods {
		if len(dirs) > 1 {
			t.Logf("pod-local pid %d in %d pods: distinct process ids", pid, len(dirs))
		}
	}
	t.Logf("identity: %d pods, %d process ids, boot ids %v", len(hosts), len(procs), boots)
}
