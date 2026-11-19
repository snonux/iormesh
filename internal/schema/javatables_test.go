package schema

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func u32(v uint32) *uint32 { return &v }

func TestParseFrame(t *testing.T) {
	cases := map[string]struct {
		frame string
		want  Method
	}{
		"full": {"iormesh.workload.scenarios.MethodFlow$Journal#create(MethodFlow.java:57)",
			Method{Class: "iormesh.workload.scenarios.MethodFlow$Journal", Method: "create",
				File: "MethodFlow.java", Line: u32(57)}},
		"no source": {"a.B#c(Unknown Source:-1)", Method{Class: "a.B", Method: "c"}},
		"native": {"java.io.FileOutputStream#writeBytes(FileOutputStream.java:-2)",
			Method{Class: "java.io.FileOutputStream", Method: "writeBytes", File: "FileOutputStream.java"}},
		"colon in file": {"a.B#<init>(c:d.java:3)", Method{Class: "a.B", Method: "<init>",
			File: "c:d.java", Line: u32(3)}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseFrame(c.frame)
			if err != nil {
				t.Fatal(err)
			}
			c.want.SchemaVersion, c.want.Frame = Version, c.frame
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
	for _, bad := range []string{"", "a.B", "#m(F.java:1)", "a.B#(F.java:1)", "a.B#m(F.java)",
		"a.B#m(F.java:x)", "a.B#m F.java:1"} {
		if _, err := ParseFrame(bad); err == nil {
			t.Errorf("ParseFrame(%q) accepted", bad)
		}
	}
}

// TestTablesRoundTrip: what TableWriter writes, ReadTable reads back.
func TestTablesRoundTrip(t *testing.T) {
	m, err := ParseFrame("a.B#c(B.java:3)")
	if err != nil {
		t.Fatal(err)
	}
	m.MethodID, m.ClassID = 1, 4
	var buf bytes.Buffer
	w := NewTableWriter[Method](&buf)
	if err := w.Write(m); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTable[Method](&buf)
	if err != nil || !reflect.DeepEqual(got, []Method{m}) {
		t.Fatalf("methods %+v, %v", got, err)
	}
	th := Thread{SchemaVersion: Version, PID: 7, PIDInc: 9, TID: 8, JavaThreadID: 1, Name: "main",
		Virtual: true}
	tw := NewTableWriter[Thread](&buf)
	if err := tw.Write(th); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	threads, err := ReadTable[Thread](&buf)
	if err != nil || !reflect.DeepEqual(threads, []Thread{th}) {
		t.Fatalf("threads %+v, %v", threads, err)
	}
}

// TestTablesReject: the writer refuses invalid entries (and keeps
// refusing), the reader refuses lines a strict reader must.
func TestTablesReject(t *testing.T) {
	var buf bytes.Buffer
	w := NewTableWriter[Method](&buf)
	if err := w.Write(Method{SchemaVersion: Version, Frame: "a.B#c(B.java:3)", Class: "a.B",
		Method: "c", File: "B.java", Line: u32(3)}); err == nil {
		t.Error("method id 0 written")
	}
	if err := w.Write(Method{}); err == nil || buf.Len() != 0 {
		t.Errorf("error did not stick or bytes written: %v, %q", err, buf.String())
	}
	lines := map[string]string{
		"parts disagree": `{"schema_version":1,"method_id":1,"frame":"a.B#c(B.java:3)","class":"x","method":"c","file":"B.java","line":3}`,
		"unknown key":    `{"schema_version":1,"method_id":1,"frame":"a.B#c(B.java:3)","class":"a.B","method":"c","file":"B.java","line":3,"jar":"x"}`,
		"missing frame":  `{"schema_version":1,"method_id":1,"class":"a.B","method":"c"}`,
		"version 2":      `{"schema_version":2,"method_id":1,"frame":"a.B#c(B.java:3)","class":"a.B","method":"c","file":"B.java","line":3}`,
	}
	for name, line := range lines {
		if _, err := ReadTable[Method](strings.NewReader(line + "\n")); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ReadTable[Thread](strings.NewReader(
		`{"schema_version":1,"pid":0,"pid_incarnation":1,"tid":1,"java_thread_id":1,"name":"x"}` + "\n")); err == nil {
		t.Error("thread pid 0 accepted")
	}
}

// TestClassTable: class rows round-trip, with and without a code source,
// and a class_id on a method row is read back (task fe4).
func TestClassTable(t *testing.T) {
	want := []Class{
		{SchemaVersion: Version, ClassID: 1, Class: "a.B", Loader: "app", CodeSource: "/opt/app/a.jar"},
		{SchemaVersion: Version, ClassID: 2, Class: "java.io.FileInputStream", Loader: "bootstrap",
			CodeSource: "jrt:/java.base"},
		{SchemaVersion: Version, ClassID: 3, Class: "$Proxy1", Loader: "app"},
	}
	var buf bytes.Buffer
	w := NewTableWriter[Class](&buf)
	for _, c := range want {
		if err := w.Write(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"code_source":"/opt/app/a.jar"`) ||
		strings.Count(buf.String(), "code_source") != 2 {
		t.Errorf("table text %q", buf.String())
	}
	got, err := ReadTable[Class](&buf)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("classes %+v, %v", got, err)
	}
	m, err := ReadTable[Method](strings.NewReader(`{"schema_version":1,"method_id":1,` +
		`"frame":"a.B#c(B.java:3)","class":"a.B","method":"c","file":"B.java","line":3,"class_id":1}` + "\n"))
	if err != nil || len(m) != 1 || m[0].ClassID != 1 {
		t.Errorf("method with class_id: %+v, %v", m, err)
	}
}

// TestClassTableReject: invalid class rows are refused by writer and
// reader alike.
func TestClassTableReject(t *testing.T) {
	rows := map[string]string{
		"id 0":        `{"schema_version":1,"class_id":0,"class":"a.B","loader":"app"}`,
		"no class":    `{"schema_version":1,"class_id":1,"class":"","loader":"app"}`,
		"no loader":   `{"schema_version":1,"class_id":1,"class":"a.B","loader":""}`,
		"missing key": `{"schema_version":1,"class_id":1,"class":"a.B"}`,
		"unknown key": `{"schema_version":1,"class_id":1,"class":"a.B","loader":"app","jar":"x"}`,
		"version 2":   `{"schema_version":2,"class_id":1,"class":"a.B","loader":"app"}`,
		"userinfo":    `{"schema_version":1,"class_id":1,"class":"a.B","loader":"app","code_source":"https://u:p@h/x.jar"}`,
		"query":       `{"schema_version":1,"class_id":1,"class":"a.B","loader":"app","code_source":"https://h/x.jar?t=1"}`,
	}
	for name, line := range rows {
		if _, err := ReadTable[Class](strings.NewReader(line + "\n")); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var buf bytes.Buffer
	if err := NewTableWriter[Class](&buf).Write(Class{SchemaVersion: Version, ClassID: 1,
		Class: "a.B", Loader: "app", CodeSource: "https://u@h/x.jar"}); err == nil || buf.Len() != 0 {
		t.Errorf("credentials written: %v, %q", err, buf.String())
	}
}

func TestCleanCodeSource(t *testing.T) {
	cases := map[string]string{
		"/opt/app/a.jar":  "/opt/app/a.jar",
		"/srv/a@b/x?.jar": "/srv/a@b/x?.jar",
		"jrt:/java.base":  "jrt:/java.base",
		"jar:file:/app.jar!/BOOT-INF/lib/x.jar!/": "jar:file:/app.jar!/BOOT-INF/lib/x.jar!/",
		"https://u:p@h:8443/x.jar?token=t#f":      "https://h:8443/x.jar",
		"jar:https://u:p@h/x.jar!/":               "jar:https://h/x.jar!/",
		"https://u@h":                             "https://h",
		"https://h/a@b/x.jar":                     "https://h/a@b/x.jar",
	}
	for in, want := range cases {
		if got := CleanCodeSource(in); got != want {
			t.Errorf("CleanCodeSource(%q) = %q, want %q", in, got, want)
		}
	}
}
