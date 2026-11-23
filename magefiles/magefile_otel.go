//go:build mage

// The OTEL-instrumented workload flavor (task za4, PLAN.md M0 catalogue):
// a separate module, workload/otel, built into its own jar so the core
// workload stays JDK-only (decision #5). Its only dependencies are the
// OpenTelemetry API jars, vendored with their license and provenance
// (workload/otel/third_party/opentelemetry/PROVENANCE) and pinned by
// SHA-256, as agent/third_party/asm is for the agent (verifyJars and
// packModuleJar, shared with workload/libs, are in magefile_libs.go).
package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const (
	otelSrcDir     = "workload/otel/src"
	otelResDir     = "workload/otel/resources"
	otelClassesDir = "workload/otel/classes"
	otelJar        = "workload-otel.jar"
	otelLibDir     = "workload/otel/third_party/opentelemetry"
)

// otelLibs are the vendored OpenTelemetry API jars and their SHA-256s:
// the API, the context module it depends on, and the small common module
// the context module depends on (their Maven dependencies).
var otelLibs = []vendoredJar{
	{"opentelemetry-api-1.67.0.jar", "538d1dff2f9a37aecbc0ef98fd17273a3affb058e734e6763a3a07a38a5fdc59"},
	{"opentelemetry-context-1.67.0.jar", "40fb813fb5e27ec2691daa29c81b572b5b1541ef9b41e62490c3ee3e6407c55e"},
	{"opentelemetry-common-1.67.0.jar", "da97bdb395418e16ecdcbe7ad3497f45b9e767ceda4ae7e86133e7a757ac377f"},
}

// WorkloadOtelBuild builds workload-otel.jar, the OTEL flavor's module:
// its classes and the ServiceLoader entry that registers the otel
// scenario with the core workload's Main. The jar holds no dependency;
// its manifest's Class-Path names workload.jar and the vendored API jars
// by their repo-relative paths, so `java -jar workload-otel.jar otel …`
// runs from the repo root like `java -jar workload.jar`.
func WorkloadOtelBuild() error {
	mg.Deps(WorkloadBuild)
	if err := verifyJars(otelLibDir, otelLibs); err != nil {
		return err
	}
	srcs, err := javaSources(otelSrcDir)
	if err != nil {
		return err
	}
	// A clean class dir keeps classes of deleted sources out of the jar.
	if err := os.RemoveAll(otelClassesDir); err != nil {
		return err
	}
	classPath := otelClassPath()
	args := append([]string{"--release", workloadRelease, "-Xlint:all", "-Werror",
		"-cp", strings.Join(classPath, string(os.PathListSeparator)),
		"-d", otelClassesDir}, srcs...)
	if err := sh.RunV("javac", args...); err != nil {
		return err
	}
	return packModuleJar(otelJar, otelClassesDir, otelResDir, classPath)
}

// otelClassPath is workload.jar and the vendored API jars, repo-relative.
func otelClassPath() []string {
	cp := []string{workloadJar}
	for _, lib := range otelLibs {
		cp = append(cp, filepath.Join(otelLibDir, lib.path))
	}
	return cp
}
