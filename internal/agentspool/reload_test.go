// An external test package: it loads the agent through
// internal/javaattach, which imports agentspool (its spool staging).
package agentspool_test

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"iormesh/internal/javaattach"
)

// TestJavaRepeatedLoadNoLeak (task 8j4): an agent loaded into a JVM that
// has one installed already must leave nothing behind. Before the fix
// every such load extracted the bridge jar again and appended it to the
// bootstrap search path, so the JVM kept one more descriptor (to the
// deleted temp jar), one more mapping and one more search-path entry for
// its life. Here the first load is -javaagent and two more come through
// the attach protocol (as `iormesh attach` and each -agent session start
// do): the JVM must still hold exactly one bridge jar descriptor, and
// both later loads must have asked the installed agent for a resync
// (task 5j4) instead.
func TestJavaRepeatedLoadNoLeak(t *testing.T) {
	jar, cp := os.Getenv("IORMESH_AGENT_JAR"), os.Getenv("IORMESH_AGENT_TEST_CP")
	if jar == "" || cp == "" {
		t.Skip("IORMESH_AGENT_JAR or IORMESH_AGENT_TEST_CP not set; run through `mage agentTest`")
	}
	cp, _, _ = strings.Cut(cp, ":") // the test classes, IoLoop
	spool := t.TempDir()
	cmd := exec.Command("java", "-javaagent:"+jar+"=spool="+spool, "-cp", cp,
		"iormesh.loop.IoLoop", t.TempDir())
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	stdin, first := startLoop(t, cmd)
	if first != "looping" {
		t.Fatalf("IoLoop: %q, want looping", first)
	}
	pid := cmd.Process.Pid
	if n := bridgeFDs(t, pid); n != 1 {
		t.Errorf("after -javaagent: %d bridge jar descriptors, want 1", n)
	}
	for load := 2; load <= 3; load++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := javaattach.New().Load(ctx, javaattach.Request{PID: uint32(pid), Jar: jar,
			Options: "spool=" + spool})
		cancel()
		if err != nil {
			t.Fatalf("load %d: %v", load, err)
		}
		if n := bridgeFDs(t, pid); n != 1 {
			t.Errorf("after load %d: %d bridge jar descriptors, want 1", load, n)
		}
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("IoLoop: %v", err)
	}
	const resync = "iormesh-agent: already installed, resyncing its ring"
	if n := strings.Count(stderr.String(), resync); n != 2 {
		t.Errorf("%d %q lines, want 2; stderr:\n%s", n, resync, stderr.String())
	}
}

// bridgeFDs counts pid's open descriptors that name an extracted bridge
// jar (Bridge.extractNested's iormesh-bridge-*.jar, deleted or not).
func bridgeFDs(t *testing.T, pid int) int {
	t.Helper()
	dir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // closed meanwhile
		}
		if strings.HasPrefix(filepath.Base(target), "iormesh-bridge-") {
			n++
		}
	}
	return n
}

// startLoop starts cmd and returns its stdin and its first stdout line
// (IoLoop prints "looping" once premain is done).
func startLoop(t *testing.T, cmd *exec.Cmd) (interface{ Close() error }, string) {
	t.Helper()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	sc := bufio.NewScanner(stdout)
	if !sc.Scan() {
		t.Fatalf("IoLoop printed nothing: %v", sc.Err())
	}
	return stdin, sc.Text()
}

// lockedBuffer collects a child's stderr: exec's copying goroutine
// writes while the test may read.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
