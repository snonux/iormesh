package containerbuild

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestSelect(t *testing.T) {
	tests := []struct {
		names   string
		want    []string
		wantErr bool
	}{
		{"", []string{"ctl", "tracer-amd64", "tracer-arm64"}, false},
		{"all", []string{"ctl", "tracer-amd64", "tracer-arm64"}, false},
		{"tracer-arm64", []string{"tracer-arm64"}, false},
		{"tracer-amd64, ctl", []string{"tracer-amd64", "ctl"}, false},
		{"tracer", nil, true},
		{"ctl,", nil, true},
	}
	for _, tt := range tests {
		got, err := Select(tt.names)
		if (err != nil) != tt.wantErr {
			t.Fatalf("Select(%q) error = %v, wantErr %v", tt.names, err, tt.wantErr)
		}
		if !slices.Equal(targetNames(got), tt.want) {
			t.Errorf("Select(%q) = %v, want %v", tt.names, targetNames(got), tt.want)
		}
	}
}

func TestCheckEngine(t *testing.T) {
	for engine, ok := range map[string]bool{"docker": true, "podman": true, "nerdctl": false, "": false} {
		if err := CheckEngine(engine); (err == nil) != ok {
			t.Errorf("CheckEngine(%q) = %v, want ok %v", engine, err, ok)
		}
	}
}

func TestTargetsBinaries(t *testing.T) {
	var names []string
	for _, tgt := range Targets() {
		for _, b := range tgt.Binaries {
			names = append(names, b.Name)
			if tgt.Tracer != b.Static {
				t.Errorf("%s: static %v, want %v (only the tracer links static)", b.Name, b.Static, tgt.Tracer)
			}
		}
	}
	want := []string{"iormeshctl-darwin-amd64", "iormeshctl-darwin-arm64",
		"iormeshctl-linux-amd64", "iormeshctl-linux-arm64",
		"iormesh-linux-amd64", "iormesh-linux-arm64"}
	if !slices.Equal(names, want) {
		t.Errorf("binaries = %v, want %v", names, want)
	}
}

func TestBuildImageArgv(t *testing.T) {
	ctl, tracer := mustTarget(t, "ctl"), mustTarget(t, "tracer-arm64")
	if argv := BuildImageArgv(Podman, ctl); argv != nil {
		t.Errorf("ctl runs the pinned golang image as is, got build %v", argv)
	}
	got := strings.Join(BuildImageArgv(Docker, tracer), " ")
	want := "docker build --platform linux/amd64 -f scripts/container/tracer-arm64.Containerfile " +
		"-t localhost/iormesh-builder:tracer-arm64 scripts/container"
	if got != want {
		t.Errorf("BuildImageArgv = %q, want %q", got, want)
	}
}

func TestRunArgvMounts(t *testing.T) {
	o := RunOptions{RepoRoot: "/r", Dist: "/r/dist", VMLinux: "/v.h", HostUID: 1000, HostGID: 100}
	tests := []struct {
		target      string
		btf, vmlinx bool
	}{
		{"ctl", false, false},
		{"tracer-amd64", true, false},
		{"tracer-arm64", true, true},
	}
	for _, tt := range tests {
		tgt := mustTarget(t, tt.target)
		argv := RunArgv(Podman, tgt, o)
		joined := strings.Join(argv, " ")
		for _, must := range []string{"--security-opt label=disable", "-v /r:/src:ro", "-v /r/dist:/out",
			"-v iormesh-gomod:/gomod", "-e GOFLAGS=-buildvcs=false"} {
			if !strings.Contains(joined, must) {
				t.Errorf("%s: argv lacks %q: %s", tt.target, must, joined)
			}
		}
		if got := strings.Contains(joined, "/sys/kernel/btf:/sys/kernel/btf:ro"); got != tt.btf {
			t.Errorf("%s: BTF mounted %v, want %v", tt.target, got, tt.btf)
		}
		if got := strings.Contains(joined, "/v.h:/vmlinux.h:ro"); got != tt.vmlinx {
			t.Errorf("%s: vmlinux.h mounted %v, want %v", tt.target, got, tt.vmlinx)
		}
		if strings.Contains(joined, "--privileged") {
			t.Errorf("%s: a build needs no privileges: %s", tt.target, joined)
		}
		if n := len(argv); argv[n-4] != tgt.Image || argv[n-3] != "sh" || argv[n-2] != "-c" {
			t.Errorf("%s: argv does not end in IMAGE sh -c SCRIPT: %v", tt.target, argv[n-4:])
		}
	}
}

func TestScript(t *testing.T) {
	o := RunOptions{RepoRoot: "/r", Dist: "/r/dist", VMLinux: "/v.h", HostUID: 1000, HostGID: 100}
	arm := mustTarget(t, "tracer-arm64")
	docker, podman := Script(Docker, arm, o), Script(Podman, arm, o)
	for _, must := range []string{"set -eu", "go run github.com/magefile/mage@v1.17.2 releaseTracer",
		"cp /vmlinux.h internal/c/vmlinux.h", "cp dist/iormesh-linux-arm64 /out/"} {
		if !strings.Contains(podman, must) {
			t.Errorf("script lacks %q:\n%s", must, podman)
		}
	}
	if !strings.Contains(docker, "chown 1000:100 /out/iormesh-linux-arm64") {
		t.Errorf("docker script must hand the binary to the host user:\n%s", docker)
	}
	if strings.Contains(podman, "chown") {
		t.Errorf("rootless podman's root is the user already; chown would pick a subuid:\n%s", podman)
	}
	if strings.Contains(podman, "generate") {
		t.Errorf("the build must use the committed generated sources:\n%s", podman)
	}
	if amd := Script(Podman, mustTarget(t, "tracer-amd64"), o); strings.Contains(amd, "vmlinux.h") {
		t.Errorf("only the arm64 build takes the vmlinux.h override:\n%s", amd)
	}
}

func TestSmokeArgv(t *testing.T) {
	b := Binary{Name: "iormesh-linux-arm64", GOOS: "linux", GOARCH: "arm64"}
	got := strings.Join(SmokeArgv(Podman, b, "/r/dist"), " ")
	want := "podman run --rm --security-opt label=disable --platform linux/arm64 -v /r/dist:/dist:ro " +
		RockyImage + " /dist/iormesh-linux-arm64 --help"
	if got != want {
		t.Errorf("SmokeArgv = %q, want %q", got, want)
	}
}

// TestContainerfilesPinTheSameImages keeps the Containerfiles' FROM lines
// and libbpfgo pins in step with the constants and with each other, and
// requires every FROM to be pinned by digest.
func TestContainerfilesPinTheSameImages(t *testing.T) {
	from := regexp.MustCompile(`(?m)^FROM (\S+)`)
	arg := regexp.MustCompile(`(?m)^ARG (LIBBPFGO_\w+=\S+)`)
	var pins []string
	for _, name := range []string{"tracer-amd64", "tracer-arm64"} {
		src, err := os.ReadFile(filepath.Join("..", "..", ContainerfileDir, name+".Containerfile"))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range from.FindAllStringSubmatch(string(src), -1) {
			if m[1] != GolangImage && m[1] != RockyImage {
				t.Errorf("%s: FROM %s is not one of the pinned images", name, m[1])
			}
		}
		var args []string
		for _, m := range arg.FindAllStringSubmatch(string(src), -1) {
			args = append(args, m[1])
		}
		if pins != nil && !slices.Equal(args, pins) {
			t.Errorf("%s: libbpfgo pins %v differ from %v", name, args, pins)
		}
		pins = args
	}
	if len(pins) != 2 {
		t.Errorf("want LIBBPFGO_TAG and LIBBPFGO_COMMIT, got %v", pins)
	}
}

func TestVerifyBinary(t *testing.T) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skipf("needs a linux/amd64 or linux/arm64 test binary, have %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	other := map[string]string{"amd64": "arm64", "arm64": "amd64"}[runtime.GOARCH]
	tests := []struct {
		b       Binary
		wantErr string
	}{
		{Binary{GOOS: "linux", GOARCH: runtime.GOARCH}, ""},
		{Binary{GOOS: "linux", GOARCH: other}, "want GOARCH " + other},
		{Binary{GOOS: "darwin", GOARCH: runtime.GOARCH}, "not a Mach-O binary"},
		{Binary{GOOS: "windows", GOARCH: runtime.GOARCH}, "no check for GOOS"},
	}
	for _, tt := range tests {
		err := VerifyBinary(self, tt.b)
		if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
			t.Errorf("VerifyBinary(%+v) = %v, want error containing %q", tt.b, err, tt.wantErr)
		}
	}
}

func TestVerifyBinaryRejectsDynamic(t *testing.T) {
	// /bin/sh is dynamically linked on every distro this suite runs on.
	b := Binary{GOOS: "linux", GOARCH: runtime.GOARCH, Static: true}
	err := VerifyBinary("/bin/sh", b)
	if err == nil || !strings.Contains(err.Error(), "dynamically linked") {
		t.Errorf("VerifyBinary(/bin/sh, static) = %v, want a dynamic-link error", err)
	}
}

func TestCheckSmokeOutput(t *testing.T) {
	if err := CheckSmokeOutput("x", "usage:\n  x run\n"); err != nil {
		t.Errorf("usage rejected: %v", err)
	}
	if err := CheckSmokeOutput("x", ""); err == nil {
		t.Error("empty output accepted")
	}
}

func mustTarget(t *testing.T, name string) Target {
	t.Helper()
	targets, err := Select(name)
	if err != nil {
		t.Fatal(err)
	}
	return targets[0]
}
