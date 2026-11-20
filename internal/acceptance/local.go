package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Ctl runs the iormeshctl under test. The production one execs the
// binary with the run's private HOME; tests fake it.
type Ctl interface {
	// Run runs iormeshctl args and returns stdout; a non-zero exit is an
	// error carrying stderr.
	Run(ctx context.Context, args ...string) (string, error)
	// Offline is Run with no way to reach a server; how says which way
	// the network was taken away.
	Offline(ctx context.Context, args ...string) (out, how string, err error)
}

// execCtl is the real Ctl.
type execCtl struct {
	bin string
	env []string
}

// NewExecCtl returns the Ctl that runs bin with HOME=home. The ssh
// control sockets stay in the operator's own cache directory on Linux
// (XDG_CACHE_HOME pinned to it): a socket path under a deep HOME could
// exceed the 104 bytes a unix socket allows.
func NewExecCtl(bin, home string) (Ctl, error) {
	env := withoutKeys(os.Environ(), "HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME")
	env = append(env, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
	if runtime.GOOS != "darwin" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		env = append(env, "XDG_CACHE_HOME="+cache)
	}
	return execCtl{bin: bin, env: env}, nil
}

func (c execCtl) Run(ctx context.Context, args ...string) (string, error) {
	return c.run(ctx, c.env, c.bin, args...)
}

// Offline runs iormeshctl in a fresh network namespace when the kernel
// lets an unprivileged user make one (unshare -rn), else with a PATH that
// holds neither ssh nor rsync — either way a build that tried to contact
// a server would fail. A build that fails inside the namespace is retried
// the PATH way (the user namespace maps the operator to root, which a
// stricter ownership check could one day refuse); how then says both.
func (c execCtl) Offline(ctx context.Context, args ...string) (string, string, error) {
	how := ""
	if _, err := c.run(ctx, c.env, "unshare", "-rn", "true"); err == nil {
		out, err := c.run(ctx, c.env, "unshare", append([]string{"-rn", c.bin}, args...)...)
		if err == nil {
			return out, "unshare -rn (no network namespace links)", nil
		}
		how = fmt.Sprintf(" (unshare -rn build failed: %v)", err)
	}
	env := append(withoutKeys(c.env, "PATH"), "PATH=/nonexistent")
	out, err := c.run(ctx, env, c.bin, args...)
	return out, "PATH without ssh and rsync" + how, err
}

func (c execCtl) run(ctx context.Context, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", filepath.Base(name),
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// withoutKeys drops the variables named keys from env.
func withoutKeys(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, key := range keys {
			drop = drop || k == key
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// snapshot hashes every regular file under root except the trees in
// skip (relative paths), so two snapshots differ exactly when a file
// outside them was added, removed or changed.
func snapshot(root string, skip ...string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		for _, s := range skip {
			if rel == s && d.IsDir() {
				return filepath.SkipDir
			}
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	return out, err
}

// diffSnapshots names the files that differ between a and b.
func diffSnapshots(a, b map[string]string) []string {
	var diff []string
	for k, v := range a {
		if b[k] != v {
			diff = append(diff, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			diff = append(diff, k)
		}
	}
	return diff
}
