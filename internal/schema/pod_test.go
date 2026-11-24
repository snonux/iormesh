package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"iormesh/internal/k8sname"
)

// The pod fields of an in-pod tracer's events (docs/milestones.md M7 "Pod
// fields on events", task sh4).
const (
	podHost   = "pod-3b1c9a52-7e0d-4c1f-9a8e-2f6d5b4c3a21"
	podCtr    = "4f6c0f1e0a3d2b8c9e7f5a6b4c3d2e1f0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d"
	podLineV1 = `"pod":{"namespace":"prod","name":"checkout-7d9f.v2","container_id":"` + podCtr + `"}}`
)

// podEvent is validEvent as an in-pod tracer writes it.
func podEvent() Event {
	e := validEvent()
	e.HostID = podHost
	e.Pod = &Pod{Namespace: "prod", Name: "checkout-7d9f.v2", ContainerID: podCtr}
	return e
}

// TestPodRoundTrip: the pod object is valid on a process event and on a
// host-level one (without a container), comes last in the hand-written
// encoder's line, and decodes back to the same event; json.Marshal
// agrees byte for byte.
func TestPodRoundTrip(t *testing.T) {
	host := hostEvent(KindSession)
	host.HostID = podHost
	host.Pod = &Pod{Namespace: "prod", Name: "checkout-7d9f.v2"}
	for name, e := range map[string]Event{"process": podEvent(), "host-level": host} {
		if err := e.Validate(); err != nil {
			t.Fatalf("%s: Validate: %v", name, err)
		}
		data := AppendEvent(nil, &e)
		std, err := json.Marshal(e)
		if err != nil || string(std) != string(data) {
			t.Errorf("%s: AppendEvent and json.Marshal differ:\n%s\n%s", name, data, std)
		}
		got, err := DecodeEvent(data)
		if err != nil || !reflect.DeepEqual(got, e) {
			t.Fatalf("%s: round trip: %+v, %v", name, got.Pod, err)
		}
	}
	e := podEvent()
	if line := string(AppendEvent(nil, &e)); !strings.HasSuffix(line, podLineV1) {
		t.Errorf("pod not last: %s", line)
	}
}

// TestPodValidateRejects: a pod object only on a pod's host id, with
// names the API server could have issued, a container id of 64 lowercase
// hex digits, and none on a host-level event.
func TestPodValidateRejects(t *testing.T) {
	cases := map[string]func(*Event){
		"machine host id":    func(e *Event) { e.HostID = "3f2a" },
		"empty namespace":    func(e *Event) { e.Pod.Namespace = "" },
		"namespace with dot": func(e *Event) { e.Pod.Namespace = "a.b" },
		"upper namespace":    func(e *Event) { e.Pod.Namespace = "Prod" },
		"long namespace":     func(e *Event) { e.Pod.Namespace = strings.Repeat("a", 64) },
		"empty name":         func(e *Event) { e.Pod.Name = "" },
		"name trailing dash": func(e *Event) { e.Pod.Name = "web-" },
		"name empty label":   func(e *Event) { e.Pod.Name = "web..x" },
		"name control":       func(e *Event) { e.Pod.Name = "web\x1b[2J" },
		"short container":    func(e *Event) { e.Pod.ContainerID = podCtr[:63] },
		"upper container":    func(e *Event) { e.Pod.ContainerID = strings.ToUpper(podCtr) },
		"host-level container": func(e *Event) {
			*e = hostEvent(KindEnd)
			e.HostID = podHost
			e.Pod = &Pod{Namespace: "prod", Name: "web", ContainerID: podCtr}
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			e := podEvent()
			breakIt(&e)
			checkRejected(t, e)
		})
	}
}

// TestPodNameRulesMatchK8sname holds the schema's byte-loop checks (the
// per-event validation cannot afford a regexp) to internal/k8sname's,
// which the tracer and the controller check the same values with.
func TestPodNameRulesMatchK8sname(t *testing.T) {
	for _, s := range []string{"", "a", "prod", "a-b", "-a", "a-", "A", "a_b", "a.b", "a..b", ".a", "a.",
		"web-7d9f.v2", strings.Repeat("a", 63), strings.Repeat("a", 64),
		strings.Repeat("a.", 126) + "a", strings.Repeat("a.", 126) + "ab", "é", "a\x00"} {
		if got, want := dnsLabel(s), k8sname.Label(s); got != want {
			t.Errorf("dnsLabel(%q) = %v, k8sname.Label %v", s, got, want)
		}
		if got, want := dnsSubdomain(s), k8sname.Pod(s); got != want {
			t.Errorf("dnsSubdomain(%q) = %v, k8sname.Pod %v", s, got, want)
		}
	}
}
