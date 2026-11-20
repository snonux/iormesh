package correlate

import (
	"reflect"
	"testing"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// TestJavaJoinClasses: frames join the class table (task fe4). Each
// method row names its class; a class is one row however many agent ids
// describe it (two threads racing on a new class); the same frame text
// of a class from another JAR is another class and another method id; a
// frame whose CLASS never arrived has no class_id; a code source with
// credentials is cleaned again before it is written.
func TestJavaJoinClasses(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(
		agentspool.Class{ID: 3, Name: "app.Main", Loader: "app", CodeSource: "/opt/app/app.jar"},
		agentspool.Class{ID: 4, Name: "app.Main", Loader: "plugins",
			CodeSource: "https://u:secret@repo/p.jar?token=t"},
		// 3 is 2 again; 4 is another JAR; 9 was never described.
		agentspool.Stack{ID: 20, Frames: []string{"java.io.FileOutputStream#writeBytes(FileOutputStream.java:-2)",
			"app.Main#save(Main.java:12)"}, Classes: []uint64{1, 3}},
		agentspool.Stack{ID: 21, Frames: []string{"java.io.FileOutputStream#writeBytes(FileOutputStream.java:-2)",
			"app.Main#save(Main.java:12)"}, Classes: []uint64{1, 4}},
		agentspool.Stack{ID: 22, Frames: []string{"java.io.FileOutputStream#writeBytes(FileOutputStream.java:-2)",
			"app.Main#save(Main.java:12)"}, Classes: []uint64{1, 9}},
		jt.bracket(1, mainTID, at-50, at+500, 10, 1),
		jt.bracket(1, mainTID, at+1000, at+1500, 20, 1),
		jt.bracket(1, mainTID, at+2000, at+2500, 21, 1),
		jt.bracket(1, mainTID, at+3000, at+3500, 22, 1),
		jt.watermark(at+20_000_000))
	for _, d := range []uint64{0, 1100, 2100, 3100} {
		jt.event(jvmPID, mainTID, "write", at+d)
	}
	jt.close()
	var stacks [][]uint64
	for _, ev := range jt.sink.events {
		if ev.Java == nil {
			t.Fatalf("event %+v not joined", ev)
		}
		stacks = append(stacks, ev.Java.Stack)
	}
	want := [][]uint64{{1, 2}, {1, 2}, {1, 3}, {1, 4}}
	if !reflect.DeepEqual(stacks, want) {
		t.Errorf("stacks %v, want %v", stacks, want)
	}
	wantClasses := []schema.Class{
		{SchemaVersion: schema.Version, ClassID: 1, Class: "java.io.FileOutputStream",
			Loader: "bootstrap", CodeSource: "jrt:/java.base"},
		{SchemaVersion: schema.Version, ClassID: 2, Class: "app.Main", Loader: "app",
			CodeSource: "/opt/app/app.jar"},
		{SchemaVersion: schema.Version, ClassID: 3, Class: "app.Main", Loader: "plugins",
			CodeSource: "https://repo/p.jar"},
	}
	if !reflect.DeepEqual(jt.tables.classes, wantClasses) {
		t.Errorf("classes %+v\nwant %+v", jt.tables.classes, wantClasses)
	}
	var classOf []uint64
	for _, m := range jt.tables.methods {
		classOf = append(classOf, m.ClassID)
	}
	if !reflect.DeepEqual(classOf, []uint64{1, 2, 3, 0}) {
		t.Errorf("method class ids %v, want [1 2 3 0]", classOf)
	}
}

// TestJavaJoinThreadRenamed: a THREAD record for a thread already known
// is a rename (task fe4): its brackets read before it keep the first
// name, those after carry the new one, the thread table gets a row per
// name, and a record repeating the name in force changes nothing and
// writes no row.
func TestJavaJoinThreadRenamed(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(
		jt.bracket(1, mainTID, at-50, at+500, 10, 1),
		agentspool.Thread{ID: 1, KTID: mainTID, Name: "worker-1"},
		agentspool.Thread{ID: 1, KTID: mainTID, Name: "worker-1"},
		jt.bracket(1, mainTID, at+1000, at+1500, 10, 1),
		jt.watermark(at+20_000_000))
	jt.event(jvmPID, mainTID, "write", at)
	jt.event(jvmPID, mainTID, "write", at+1100)
	jt.close()
	var names []string
	for _, ev := range jt.sink.events {
		if ev.Java == nil {
			t.Fatalf("event %+v not joined", ev)
		}
		names = append(names, ev.Java.Thread)
	}
	if !reflect.DeepEqual(names, []string{"main", "worker-1"}) {
		t.Errorf("thread names %v, want [main worker-1]", names)
	}
	if n := len(jt.tables.threads); n != 2 || jt.tables.threads[1].Name != "worker-1" ||
		jt.tables.threads[1].JavaThreadID != 1 {
		t.Errorf("thread table %+v", jt.tables.threads)
	}
}

// TestJavaJoinRenameWithoutBrackets: a rename of a thread with no bracket
// held (pruned, or none yet) names every later bracket.
func TestJavaJoinRenameWithoutBrackets(t *testing.T) {
	v := newJVM(nil, jvmPID, jvmStart, 0, 0)
	first := agentspool.Thread{ID: 5, KTID: 9, Name: "a"}
	v.threads[5] = first
	v.rename(first, "b")
	if got := v.nameAt(first, 1); got != "b" {
		t.Errorf("name %q, want b", got)
	}
	virt := agentspool.Thread{ID: 6, Name: "", Virtual: true}
	v.addBracket(&agentspool.Bracket{Hook: 1, KTID: 3, EnterMono: 1, ExitMono: 40, ThreadID: 6})
	v.addBracket(&agentspool.Bracket{Hook: 1, KTID: 4, EnterMono: 1, ExitMono: 70, ThreadID: 7})
	v.rename(virt, "v")
	if got, old := v.nameAt(virt, 41), v.nameAt(virt, 40); got != "v" || old != "" {
		t.Errorf("virtual thread names %q at 41, %q at 40", got, old)
	}
}
