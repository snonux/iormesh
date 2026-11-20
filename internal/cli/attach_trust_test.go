package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTrustedAgent: the jar root loads into every JVM must be changeable
// by nobody but root and the caller — itself and every directory above
// it (task ge4's security review, N1). The test's temporary directory
// is the caller's, so only the modes below it vary.
func TestTrustedAgent(t *testing.T) {
	for _, tc := range []struct {
		name             string
		dirMode, jarMode os.FileMode
		ok               bool
	}{
		{"private", 0o755, 0o644, true},
		{"group-writable jar", 0o755, 0o664, false},
		{"other-writable jar", 0o755, 0o646, false},
		{"group-writable dir", 0o775, 0o644, false},
		{"sticky shared dir", 0o777 | os.ModeSticky, 0o644, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "agent")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			jar := filepath.Join(dir, "iormesh-agent.jar")
			if err := os.WriteFile(jar, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			// Chmod, not the create modes: the umask would mask them.
			if err := os.Chmod(jar, tc.jarMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, tc.dirMode); err != nil {
				t.Fatal(err)
			}
			got, err := trustedAgent(jar)
			if (err == nil) != tc.ok {
				t.Fatalf("trustedAgent = %q, %v; want ok %v", got, err, tc.ok)
			}
		})
	}
}

// TestTrustedAgentResolvesSymlinks: the JVM gets the checked file's real
// path, so a symlink re-pointed after the check changes nothing.
func TestTrustedAgentResolvesSymlinks(t *testing.T) {
	agent, _ := agentFiles(t)
	link := filepath.Join(filepath.Dir(agent), "link.jar")
	if err := os.Symlink(agent, link); err != nil {
		t.Fatal(err)
	}
	got, err := trustedAgent(link)
	if err != nil || got != agent {
		t.Errorf("trustedAgent(link) = %q, %v; want %q", got, err, agent)
	}
	if _, err := trustedAgent(filepath.Dir(agent)); err == nil {
		t.Error("a directory passed as the agent jar")
	}
}
