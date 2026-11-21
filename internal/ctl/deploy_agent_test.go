package ctl

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/release"
)

// agentFlags are a project's flags that use the installed Java agent.
var agentFlags = []string{"-spool", "/run/iormesh/spool/jvm", "-agent", installedAgent}

// agentFixture is the deploy fixture with a project "jvm" on app1 that
// uses the agent, added after shop and billing.
func agentFixture(t *testing.T) *deployFixture {
	t.Helper()
	f := newDeployFixture(t)
	f.mustRun(t, slices.Concat([]string{"add", "-project", "jvm", "-pattern", "*java*", "app1"}, agentFlags)...)
	return f
}

// agentPreflightLog is preflightLog for a host that also gets the jar:
// one push of both files with their signatures, the tracer's upgrade,
// then the jar's.
func agentPreflightLog(host, arch string) [][]string {
	name := "iormesh-linux-" + arch
	log := preflightLog(host, arch)
	n := len(log)
	log[n-2] = call(host, "push", staging, name, name+".minisig", release.AgentFile,
		release.AgentFile+".minisig")
	return append(log, sudoCall(host, "upgrade", staging+"/"+release.AgentFile))
}

// TestDeployShipsAgent (task kf4): a host with a project that uses the
// agent gets the verified jar staged with the tracer and installed by
// the tracer's own upgrade, after the tracer's; then the projects start,
// the agent project with its -spool and the installed jar as -agent. A
// host without such a project gets no jar.
func TestDeployShipsAgent(t *testing.T) {
	f := agentFixture(t)
	if code := f.run("deploy", "-dist", f.dist, "app1", "app2"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	checkLog(t, f.log,
		agentPreflightLog("app1", "amd64"),
		startLog("app1", "shop", "start", "-project", "shop", "-pattern=*orders*", "-duration", "1h"),
		startLog("app1", "billing", "start", "-project", "billing", "-listen-port=8443"),
		startLog("app1", "jvm", slices.Concat([]string{"start", "-project", "jvm", "-pattern=*java*"},
			agentFlags)...),
		preflightLog("app2", "amd64"),
		startLog("app2", "shop", "start", "-project", "shop", "-pattern=*orders*"))
	if !strings.Contains(f.stdout.String(), "iormesh-agent.jar 0.4.0") {
		t.Errorf("stdout lacks the jar's verification:\n%s", f.stdout)
	}
}

// TestDeployRefusesBadAgent: a missing, unsigned, tampered or
// foreign-signed jar, or one signed at another version than the tracer,
// fails the host after the preflight: nothing pushed, nothing sudo'd,
// nothing started — the tracer is not even upgraded.
func TestDeployRefusesBadAgent(t *testing.T) {
	_, other := genKey(t)
	type spoiler func(t *testing.T, f *deployFixture, jar string)
	cases := map[string]struct {
		spoil spoiler
		want  string // the reason stderr must give
	}{
		"missing jar": {func(t *testing.T, _ *deployFixture, jar string) { rm(t, jar) }, "no such file"},
		"missing signature": {func(t *testing.T, _ *deployFixture, jar string) {
			rm(t, jar+release.SigSuffix)
		}, "no such file"},
		"tampered jar": {func(t *testing.T, _ *deployFixture, jar string) {
			writeFile(t, jar, []byte("evil classes"), 0o644)
		}, "does not match its signature"},
		"foreign key": {func(t *testing.T, _ *deployFixture, jar string) { signed(t, other, jar) },
			"the pinned key is"},
		"other version": {func(t *testing.T, f *deployFixture, jar string) {
			signedAs(t, f.priv, jar, "0.3.0")
		}, "is signed as 0.3.0, the tracer as 0.4.0"},
		"tracer renamed as the jar": {func(t *testing.T, f *deployFixture, jar string) {
			bin := filepath.Join(f.dist, "iormesh-linux-amd64")
			copyFile(t, bin, jar)
			copyFile(t, bin+release.SigSuffix, jar+release.SigSuffix)
		}, "was signed as iormesh-linux-amd64"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := agentFixture(t)
			c.spoil(t, f, filepath.Join(f.dist, release.AgentFile))
			code := f.run("deploy", "-dist", f.dist, "-project", "jvm", "app1")
			stderr := f.stderr.String()
			if code != exitError || !strings.Contains(stderr, "project jvm uses the Java agent") ||
				!strings.Contains(stderr, "nothing pushed") || !strings.Contains(stderr, c.want) {
				t.Fatalf("exit %d, stderr %s, want %q", code, stderr, c.want)
			}
			checkLog(t, f.log, preflightLog("app1", "amd64")[:4])
		})
	}
}

// TestDeployAgentUpgradeRefused: the server refusing the jar (its own
// check against the root-owned key, or a version mismatch) starts
// nothing, though the tracer's upgrade went through.
func TestDeployAgentUpgradeRefused(t *testing.T) {
	f := agentFixture(t)
	f.answers["app1 sudo -n /opt/iormesh/bin/iormesh upgrade "+staging+"/"+release.AgentFile] = reply{
		err: errors.New("exit status 1: signature verification failed")}
	code := f.run("deploy", "-dist", f.dist, "app1")
	if code != exitError || !strings.Contains(f.stderr.String(),
		"upgrade of iormesh-agent.jar refused, nothing started: exit status 1: signature verification failed") {
		t.Fatalf("exit %d, stderr %s", code, f.stderr)
	}
	checkLog(t, f.log, agentPreflightLog("app1", "amd64"))
}

// TestAgentProject: -spool or -agent, in any spelling, marks a project as
// one that needs the jar; a value that happens to read "spool" does not.
func TestAgentProject(t *testing.T) {
	for flags, want := range map[string]bool{
		"-spool /run/iormesh/spool/p": true, "--spool=/run/iormesh/spool/p": true,
		"-agent=" + installedAgent: true, "-duration 1h": false, "-families spool": false, "": false,
	} {
		ps := []federate.Project{{Name: "p", Flags: strings.Fields(flags)}}
		if got := agentProject(ps) != ""; got != want {
			t.Errorf("%q: needs agent %v, want %v", flags, got, want)
		}
	}
}

// copyFile copies src's bytes to dst.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dst, data, 0o644)
}
