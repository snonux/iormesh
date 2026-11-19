//go:build mage

// WorkloadTest checks for M3's method-flow and HTTP scenarios (task ya4)
// and the OTEL flavor (task za4): the HTTP pairs run as two processes the
// way tcp-loopback does, and each scenario's malformed and failing
// invocations exit with the right code.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/magefile/mage/sh"
)

// m3UsageErrors are malformed M3 invocations (exit 2): method-flow is
// solo with --dir only; the HTTP scenarios are client/server, the client
// needs a connectable --port, and only http-tls takes --dir.
var m3UsageErrors = [][]string{
	{"method-flow", "--role", "client"},
	{"method-flow", "--port", "1"},
	{"http-plain", "--role", "solo"},
	{"http-plain", "--role", "client"},
	{"http-plain", "--role", "client", "--port", "0"},
	{"http-plain", "--role", "server", "--dir", "x"},
	{"http-redaction", "--role", "solo"},
	{"http-redaction", "--role", "client", "--port", "abc"},
	{"http-tls", "--role", "solo"},
	{"http-tls", "--role", "server", "--bogus", "x"},
}

// checkM3Scenarios runs the HTTP pairs, the run-time failures, then the
// OTEL flavor from its own jar.
func checkM3Scenarios() error {
	for _, name := range []string{"http-plain", "http-redaction"} {
		if err := checkTwoRoles(name, nil, portClient); err != nil {
			return err
		}
	}
	if err := checkHTTPTLS(); err != nil {
		return err
	}
	if err := checkM3RunFailures(); err != nil {
		return err
	}
	return checkOtelScenario()
}

// checkHTTPTLS runs http-tls's two roles in one shared --dir: the server
// writes its public certificate there before its listening line, the
// client pins it from there, and the server removes it once served.
func checkHTTPTLS() error {
	dir, err := os.MkdirTemp("", "iormesh-http-tls-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	err = checkTwoRoles("http-tls", []string{"--dir", dir}, func(addr string) []string {
		opts := portClient(addr)
		if opts == nil {
			return nil
		}
		return append(opts, "--dir", dir)
	})
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "http-tls.crt")); !os.IsNotExist(err) {
		return fmt.Errorf("http-tls left its certificate behind (stat: %v)", err)
	}
	return nil
}

// checkM3RunFailures: well-formed runs that fail at run time exit 1 — a
// client with nobody listening, an http-tls client whose --dir holds no
// certificate (it must never fall back to trusting anything else), and
// method-flow with a --dir that does not exist.
func checkM3RunFailures() error {
	port, err := closedPort()
	if err != nil {
		return err
	}
	empty, err := os.MkdirTemp("", "iormesh-no-cert-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(empty) }()
	failing := [][]string{
		{"http-plain", "--role", "client", "--port", fmt.Sprint(port)},
		{"http-tls", "--role", "client", "--port", fmt.Sprint(port), "--dir", empty},
		{"method-flow", "--dir", "/nonexistent/iormesh-workload"},
	}
	for _, args := range failing {
		if err := expectExit(1, args...); err != nil {
			return err
		}
	}
	return nil
}

// otelUsageErrors are malformed invocations of the OTEL flavor (exit 2),
// run from its own jar: client/server only, the client needs a --port,
// and it takes no --dir.
var otelUsageErrors = [][]string{
	{"otel", "--role", "solo"},
	{"otel", "--role", "client"},
	{"otel", "--role", "server", "--dir", "x"},
	{"--self-test", "otel", "no-such-scenario"},
}

// checkOtelScenario runs the OTEL flavor from workload-otel.jar (task
// za4): its self-test alone (the core scenarios already ran), --list and
// --expect through the core Main, the two-process pair — whose server
// fails the run unless the client's traceparent and tracestate arrive as
// declared — its usage errors, and a client with nobody listening.
func checkOtelScenario() error {
	if err := sh.RunV("java", "-jar", otelJar, "--self-test", "otel"); err != nil {
		return err
	}
	if err := checkOtelListed(); err != nil {
		return err
	}
	if err := checkTwoRolesJar(otelJar, "otel", nil, portClient); err != nil {
		return err
	}
	for _, args := range otelUsageErrors {
		if err := expectExitJar(otelJar, nil, 2, args...); err != nil {
			return err
		}
	}
	port, err := closedPort()
	if err != nil {
		return err
	}
	return expectExitJar(otelJar, nil, 1, "otel", "--role", "client", "--port", fmt.Sprint(port))
}

// checkOtelListed: the jar's --list names otel beside the core
// scenarios, and --expect otel prints the golden bundled in workload.jar
// — while workload.jar alone, with no OpenTelemetry on its class path,
// does not know the scenario.
func checkOtelListed() error {
	out, err := runBoundedJar(otelJar, nil, "--list")
	if err != nil {
		return fmt.Errorf("%s --list: %w", otelJar, err)
	}
	names := strings.Fields(string(out))
	if !slices.Contains(names, "otel") || !slices.Contains(names, "http-plain") {
		return fmt.Errorf("%s --list: want otel and the core scenarios, got %q", otelJar, names)
	}
	exp, err := runBoundedJar(otelJar, nil, "--expect", "otel")
	if err != nil || !strings.Contains(string(exp), `"scenario": "otel"`) {
		return fmt.Errorf("%s --expect otel: %q, %v", otelJar, exp, err)
	}
	return expectExit(2, "otel", "--role", "server")
}
