package k8s

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKubectl is a kubectl stand-in on PATH: it appends its argv to a log
// (each element NUL-terminated, each call closed by an RS element) and
// serves canned output — the pod JSON for `get`, the tar for `exec … ready`
// without -remove, a copy of the --custom file for `debug` — and exits with
// $FAKE_KUBECTL_EXIT. No cluster is reached; the tests below assert the
// exact argv the real kubectl would get.
const fakeKubectl = `#!/bin/sh
for a in "$@"; do printf '%s\0' "$a" >> "$FAKE_KUBECTL_LOG"; done
printf '\036\0' >> "$FAKE_KUBECTL_LOG"
verb=""; remove=""; ready=""
for a in "$@"; do
  case "$a" in
    get|exec|debug) [ -z "$verb" ] && verb="$a" ;;
    ready) ready=1 ;;
    -remove) remove=1 ;;
    --custom=*) cp "${a#--custom=}" "$FAKE_KUBECTL_LOG.custom" ;;
  esac
done
case "$verb" in
  get) cat "$FAKE_KUBECTL_POD" ;;
  exec) if [ -n "$ready" ] && [ -z "$remove" ]; then cat "$FAKE_KUBECTL_TAR"; else echo ran; fi ;;
esac
echo "fake kubectl stderr" >&2
exit "${FAKE_KUBECTL_EXIT:-0}"
`

// fake is one test's fake kubectl: its log, canned pod JSON and tar.
type fake struct {
	dir string
	log string
}

// newFake puts the fake kubectl first on PATH for this test.
func newFake(t *testing.T) *fake {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(fakeKubectl), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fake{dir: dir, log: filepath.Join(dir, "log")}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_KUBECTL_LOG", f.log)
	t.Setenv("FAKE_KUBECTL_POD", filepath.Join(dir, "pod.json"))
	t.Setenv("FAKE_KUBECTL_TAR", filepath.Join(dir, "stream.tar"))
	t.Setenv("FAKE_KUBECTL_EXIT", "0")
	return f
}

// pod sets the JSON `kubectl get pod` serves.
func (f *fake) pod(t *testing.T, js string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "pod.json"), []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
}

// tar sets the stream `iormesh ready` serves.
func (f *fake) tar(t *testing.T, b []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "stream.tar"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// calls parses the log into one argv per kubectl invocation.
func (f *fake) calls(t *testing.T) [][]string {
	t.Helper()
	b, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var cur []string
	for _, a := range bytes.Split(bytes.TrimSuffix(b, []byte{0}), []byte{0}) {
		if string(a) == "\x1e" {
			calls = append(calls, cur)
			cur = nil
			continue
		}
		cur = append(cur, string(a))
	}
	return calls
}

// custom returns the --custom file the last debug call carried.
func (f *fake) custom(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.log + ".custom")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// testImage is a digest-pinned image (a placeholder repository).
const testImage = "registry.example/iormesh@sha256:" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// testTarget is a valid debug-mode target (placeholders only, #18).
func testTarget() Target {
	return Target{Context: "kind-iormesh", Namespace: "shop", Pod: "checkout-7d9f",
		Container: "app", Image: testImage, Mode: ModeDebug, Profile: ProfileMinimal}
}

// newTestKubectl is New on target t, failing the test on refusal.
func newTestKubectl(t *testing.T, tg Target) *Kubectl {
	t.Helper()
	k, err := New(tg)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
