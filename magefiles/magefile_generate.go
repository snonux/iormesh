//go:build mage

// Adapted from ior Magefile.go @ b21afc50328c (Generate*, the result-file
// diff gate and their helpers).
// Changes: IOR_FORCE_GENERATE renamed to IORMESH_FORCE_GENERATE; split out
// of the main Magefile; long helpers split under 50 lines; a failing
// scan or sort of the result lines is an error instead of a silent fallback;
// Generate also writes the syscall number tables (task 8e4);
// GenerateTracepointsGo reads only the C, the list needs no kinds (task hj4).

package main

import (
	"bufio"
	"errors"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"

	"iormesh/internal/generate"
)

// Generate regenerates the C tracepoint handlers, the Go tracepoint list,
// the Go types from the running kernel's syscall tracepoints, and the
// per-arch syscall number tables of the raw_syscalls dispatch
// (magefile_sysnum.go).
//
// The C step is diff-gated: if the derived result file would change (another
// kernel, another tracepoint set), it aborts without touching the tree.
// IORMESH_FORCE_GENERATE=1/yes/true bypasses the gate.
func Generate() error {
	fmt.Println("Generating tracepoint and type artifacts...")
	if forceGenerateFromEnv() {
		fmt.Println("Force generation enabled – ignoring diff checks.")
		mg.SerialDeps(GenerateTracepointsCForce, GenerateTracepointsGo, GenerateTypesGo, GenerateSyscallNumbersGo)
	} else {
		mg.SerialDeps(GenerateTracepointsC, GenerateTracepointsGo, GenerateTypesGo, GenerateSyscallNumbersGo)
	}
	fmt.Println("Generation complete.")
	return nil
}

// forceGenerateFromEnv parses IORMESH_FORCE_GENERATE strictly: only
// 1/yes/true force; 0/no/false keep the gate, and an unknown value warns
// instead of silently forcing.
func forceGenerateFromEnv() bool {
	switch value := strings.ToLower(strings.TrimSpace(os.Getenv(forceGenerateEnv))); value {
	case "", "0", "no", "false":
		return false
	case "1", "yes", "true":
		return true
	default:
		fmt.Printf("Warning: ignoring unknown %s value %q (use 1/yes/true to force).\n",
			forceGenerateEnv, value)
		return false
	}
}

// GenerateTracepointsC regenerates the tracepoint handlers in C (diff-gated).
func GenerateTracepointsC() error {
	fmt.Println("Generating C tracepoints...")
	return generateTracepointsC(true, false)
}

// GenerateTracepointsCForce regenerates the tracepoint handlers in C, ignoring diffs.
func GenerateTracepointsCForce() error {
	fmt.Println("Generating C tracepoints (force)...")
	return generateTracepointsC(false, false)
}

// GenerateTracepointsCStdout prints the tracepoint handlers in C to stdout.
func GenerateTracepointsCStdout() error {
	fmt.Println("Generating C tracepoints (stdout)...")
	return generateTracepointsC(true, true)
}

// GenerateTracepointsGo regenerates the tracepoint list in Go from the
// generated C: List only, the attach families come from
// internal/tracepoints/trace_family.go, not from generated maps.
func GenerateTracepointsGo() error {
	fmt.Println("Generating Go tracepoints list...")
	input, err := os.ReadFile(tracepointsCPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", tracepointsCPath, err)
	}
	output, err := generate.ExtractTracepoints(strings.NewReader(string(input)))
	if err != nil {
		return err
	}
	return writeFormattedGo(tracepointsGoPath, output)
}

// GenerateTypesGo regenerates the Go types and constants from types.h and
// the generated C.
func GenerateTypesGo() error {
	fmt.Println("Generating Go types...")
	input, err := readTypesInput()
	if err != nil {
		return err
	}
	structs, constants, err := generate.ParseCTypesInput(strings.NewReader(input))
	if err != nil {
		return err
	}
	output := generate.AddTypesImports(generate.GenerateTypesGo(structs, constants))
	return writeFormattedGo(typesGoPath, output)
}

func writeFormattedGo(path, src string) error {
	formatted, err := format.Source([]byte(src))
	if err != nil {
		return fmt.Errorf("format %s: %w", path, err)
	}
	if err := os.WriteFile(path, formatted, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func generateTracepointsC(strict bool, toStdout bool) error {
	formats, err := readSyscallFormats()
	if err != nil {
		return err
	}
	parsed, err := generate.ParseFormats(strings.NewReader(formats))
	if err != nil {
		return err
	}
	output := generate.GenerateTracepointsC(parsed)
	if toStdout {
		fmt.Print(output)
		return nil
	}
	return replaceTracepointsC(output, strict)
}

// replaceTracepointsC renders to a temp file and runs the strict result gate
// BEFORE replacing the committed C file, so a failing generate (e.g. on a
// kernel whose tracepoint set differs from the committed one) leaves the
// working tree untouched.
func replaceTracepointsC(output string, strict bool) error {
	tmpCPath, err := writeTempRender(filepath.Dir(tracepointsCPath), filepath.Base(tracepointsCPath), output)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tmpCPath)
		_ = os.Remove(tracepointsResultNew)
	}()
	if err := stageTracepointsResultGate(output, strict); err != nil {
		return err
	}
	if err := os.Rename(tmpCPath, tracepointsCPath); err != nil {
		return fmt.Errorf("replace %s: %w", tracepointsCPath, err)
	}
	return adoptTracepointsResult()
}

// writeTempRender writes content to a 0644 temp file in dir whose name starts
// with base, so the later rename lands atomically in the target directory.
func writeTempRender(dir, base, content string) (string, error) {
	tmp, err := os.CreateTemp(dir, base+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temp render: %w", err)
	}
	_, err = tmp.WriteString(content)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("write temp render: %w", err)
	}
	return tmp.Name(), nil
}

// stageTracepointsResultGate renders the .new result transient and, in strict
// mode, fails when it would change the committed result file. The caller's
// cleanup removes the transients; nothing else is written.
func stageTracepointsResultGate(output string, strict bool) error {
	result, err := extractTracepointReasons(output)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tracepointsResultNew, []byte(result), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tracepointsResultNew, err)
	}
	if _, err := os.Stat(tracepointsResult); errors.Is(err, os.ErrNotExist) {
		return nil // bootstrap: no committed result to gate against yet
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", tracepointsResult, err)
	}
	err = sh.RunV("diff", "-u", tracepointsResult, tracepointsResultNew)
	if err == nil || !strict {
		return nil
	}
	return fmt.Errorf("generated tracepoint set differs from %s (another kernel?); "+
		"set %s=1 to adopt it: %w", tracepointsResult, forceGenerateEnv, err)
}

// adoptTracepointsResult moves the staged .new result onto the committed
// result file.
func adoptTracepointsResult() error {
	if err := sh.RunV("cp", tracepointsResultNew, tracepointsResult); err != nil {
		return err
	}
	return os.Remove(tracepointsResultNew)
}

// extractTracepointReasons collects the generator's "/// " reason lines,
// sorted in the C locale so the result file is byte-stable for a kernel.
func extractTracepointReasons(output string) (string, error) {
	var reasons []string
	sc := bufio.NewScanner(strings.NewReader(output))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if reason, ok := strings.CutPrefix(sc.Text(), "/// "); ok {
			reasons = append(reasons, reason)
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("scan generated C: %w", err)
	}
	if len(reasons) == 0 {
		return "", nil
	}
	return sortLinesWithLocale(reasons)
}

func sortLinesWithLocale(lines []string) (string, error) {
	cmd := exec.Command("sort")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("sort result lines: %w", err)
	}
	return string(output), nil
}

// readSyscallFormats reads every syscall tracepoint format with one sudo
// call; LC_ALL=C find | sort fixes the order so generation is deterministic
// for a given kernel.
func readSyscallFormats() (string, error) {
	fmt.Println("Reading syscall format files with one sudo call...")
	output, err := sudoOutput("sh", "-c", "LC_ALL=C find /sys/kernel/tracing/events/syscalls -maxdepth 2 -mindepth 2 -name format | sort | xargs cat")
	if err != nil {
		return "", err
	}
	if output == "" {
		return "", fmt.Errorf("no syscall format files found")
	}
	return output, nil
}

func readTypesInput() (string, error) {
	var b strings.Builder
	for _, p := range []string{typesHeaderPath, tracepointsCPath} {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", p, err)
		}
		b.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}
