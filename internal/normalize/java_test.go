package normalize

import (
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// javaMethods is a method table: the JDK frames and the scenario's.
func javaMethods(t *testing.T) map[uint64]schema.Method {
	t.Helper()
	out := map[uint64]schema.Method{}
	for i, f := range []string{
		"java.io.FileOutputStream#writeBytes(FileOutputStream.java:-2)",
		"sun.nio.ch.FileChannelImpl#write(FileChannelImpl.java:300)",
		"iormesh.workload.scenarios.MethodFlow$Journal#append(MethodFlow.java:80)",
		"iormesh.workload.Main#main(Main.java:40)",
	} {
		m, err := schema.ParseFrame(f)
		if err != nil {
			t.Fatal(err)
		}
		m.MethodID = uint64(i + 1)
		out[m.MethodID] = m
	}
	return out
}

func TestCausingMethod(t *testing.T) {
	methods := javaMethods(t)
	cases := map[string]struct {
		java *schema.Java
		want string
	}{
		"skips JDK frames": {&schema.Java{Stack: []uint64{1, 2, 3, 4}},
			"iormesh.workload.scenarios.MethodFlow$Journal#append"},
		"innermost wins": {&schema.Java{Stack: []uint64{4, 3}}, "iormesh.workload.Main#main"},
		"only JDK":       {&schema.Java{Stack: []uint64{1, 2}}, ""},
		"unknown id":     {&schema.Java{Stack: []uint64{1, 9, 3}}, ""},
		"no attribution": {nil, ""},
	}
	for name, c := range cases {
		if got := CausingMethod(schema.Event{Java: c.java}, methods); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// TestCheckJava: a java declaration needs the thread name and the causing
// method; the attribution alone does not satisfy it.
func TestCheckJava(t *testing.T) {
	ev := schema.Event{SchemaVersion: 1, Seq: 1, PID: 1, TID: 1, Event: "write", FD: 3,
		Attribution: schema.AttributionJava,
		Java:        &schema.Java{Thread: "main", Stack: []uint64{1, 3, 4}}}
	got := Trace{Events: []schema.Event{ev}, Methods: javaMethods(t)}
	doc := func(java string) Expectation {
		x, err := ParseExpectation([]byte(`{"scenario": "s", "schema_version": 1, "events": [
		  {"thread": "$t1", "event": "write", "attribution": "java", "java": ` + java + `}],
		  "edges": [], "must_not_appear": []}`))
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	pass := `{"thread": "main", "method": "iormesh.workload.scenarios.MethodFlow$Journal#append"}`
	if vs := Check(got, doc(pass)); len(vs) != 0 {
		t.Fatalf("violations %v", vs)
	}
	for _, fail := range []string{
		`{"thread": "worker", "method": "iormesh.workload.scenarios.MethodFlow$Journal#append"}`,
		`{"method": "iormesh.workload.Main#main"}`,
	} {
		vs := Check(got, doc(fail))
		if len(vs) != 1 || !strings.Contains(vs[0].Msg, "java") {
			t.Errorf("%s: violations %v", fail, vs)
		}
	}
	got.Methods = nil // a trace without its method table cannot satisfy it
	if vs := Check(got, doc(pass)); len(vs) != 1 {
		t.Errorf("without methods: violations %v", vs)
	}
}

func TestExpectationJavaRejects(t *testing.T) {
	for _, ev := range []string{
		`{"thread": "$t1", "event": "write", "java": {"method": "Main"}}`,
		`{"thread": "$t1", "event": "write", "java": {"method": "a.B#c(B.java:3)"}}`,
		`{"thread": "$t1", "event": "write", "attribution": "none", "java": {"method": "a.B#c"}}`,
	} {
		if _, err := ParseExpectation([]byte(`{"scenario": "s", "schema_version": 1, "events": [` +
			ev + `], "edges": [], "must_not_appear": []}`)); err == nil {
			t.Errorf("%s accepted", ev)
		}
	}
}
