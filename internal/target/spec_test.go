package target

import (
	"errors"
	"slices"
	"testing"
)

// restartArgv is the command line ChildJvm builds for RestartApp.
var restartArgv = []string{"/usr/lib/jvm/java-25/bin/java", "-XX:+UseSerialGC",
	"-XX:TieredStopAtLevel=1", "-XX:-UsePerfData", "-cp", "/build/workload.jar",
	"iormesh.workload.scenarios.RestartApp", "/tmp/it"}

func TestSpecMatches(t *testing.T) {
	spec := Spec{Pattern: "*iormesh.workload.scenarios.RestartApp*"}
	cases := []struct {
		name string
		exe  string
		argv []string
		want bool
	}{
		{"argv match", restartArgv[0], restartArgv, true},
		{"launcher never matches", "/usr/bin/java",
			[]string{"java", "-jar", "/build/workload.jar", "restart", "--dir", "/tmp/it"}, false},
		{"non-matching exec", "/usr/bin/ls", []string{"ls", "-l"}, false},
		// What a posix_spawn child execs first, before the app itself.
		{"jspawnhelper", "/usr/lib/jvm/java-25/lib/jspawnhelper",
			[]string{"/usr/lib/jvm/java-25/lib/jspawnhelper", "3:4:5"}, false},
		// Only the comm would fit (the kernel's comm of a JVM is "java",
		// at most 15 characters): no exe, no argv, no match.
		{"comm-only process", "", nil, false},
		{"empty argv, exe does not match", "/usr/bin/java", nil, false},
	}
	for _, c := range cases {
		if got := spec.Matches(c.exe, c.argv); got != c.want {
			t.Errorf("%s: Matches = %v, want %v", c.name, got, c.want)
		}
	}
	exeSpec := Spec{Pattern: "*/bin/echopeer"}
	if !exeSpec.Matches("/build/bin/echopeer", []string{"echopeer"}) {
		t.Error("a pattern on the executable path did not match the exe")
	}
}

// TestSpecMatchesRejectsCommOnly: the comm a JVM shows ("java", or a
// 15-character cut of a thread name) is never what the pattern is tried
// on, even when it alone would fit.
func TestSpecMatchesRejectsCommOnly(t *testing.T) {
	spec := Spec{Pattern: "*RestartApp*"}
	comm := "RestartApp" // what a renamed main thread could show
	if spec.Matches("", []string{}) {
		t.Fatal("matched a process with neither exe nor argv")
	}
	if !spec.Matches("", []string{comm}) {
		t.Fatal("control: the same string as argv must match")
	}
}

func TestSpecValidate(t *testing.T) {
	good := []Spec{{PIDs: []uint32{1}}, {Pattern: "*x*"}, {ListenPort: 8080}}
	for _, s := range good {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v", s, err)
		}
	}
	bad := []Spec{{}, {PIDs: []uint32{1}, Pattern: "*x*"}, {Pattern: "*x*", ListenPort: 1},
		{PIDs: []uint32{1}, ListenPort: 1}, {Pattern: "*"}}
	for _, s := range bad {
		if err := s.Validate(); !errors.Is(err, ErrSpec) {
			t.Errorf("Validate(%+v) = %v, want ErrSpec", s, err)
		}
	}
}

func TestSpecMode(t *testing.T) {
	if (Spec{PIDs: []uint32{1}}).Mode() != ModePIDs || (Spec{Pattern: "a"}).Mode() != ModePattern ||
		(Spec{ListenPort: 1}).Mode() != ModeListenPort {
		t.Fatal("Mode does not follow the field that is set")
	}
}

func TestSplitArgv(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"java\x00-cp\x00a.jar\x00", []string{"java", "-cp", "a.jar"}},
		{"java\x00x\x00\x00\x00", []string{"java", "x"}},
		{"setproctitle style", []string{"setproctitle style"}},
		{"", nil},
		{"\x00\x00", nil},
	}
	for _, c := range cases {
		if got := SplitArgv([]byte(c.in)); !slices.Equal(got, c.want) {
			t.Errorf("SplitArgv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
