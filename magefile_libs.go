//go:build mage

// The third-party HTTP workload module (task de4, PLAN.md decision #22's
// Servlet, Apache HttpClient, OkHttp and Netty): workload/libs, built into
// its own jar like the OTEL flavor (magefile_otel.go), so the core
// workload stays JDK-only (decision #5). Its dependencies are vendored
// under workload/libs/third_party/<library>, each directory with the
// library's license and a PROVENANCE file, every jar pinned by SHA-256.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const (
	libsSrcDir     = "workload/libs/src"
	libsResDir     = "workload/libs/resources"
	libsClassesDir = "workload/libs/classes"
	libsJar        = "workload-libs.jar"
	libsVendorDir  = "workload/libs/third_party"
)

// vendoredJar is one third-party jar: its path below a vendor directory
// and its SHA-256 (also in that library's PROVENANCE).
type vendoredJar struct{ path, sha256 string }

// libsJars are workload/libs's vendored jars: Tomcat's embeddable core
// (the Servlet API and container) with the annotations API it depends on.
var libsJars = []vendoredJar{
	{"tomcat/tomcat-embed-core-10.1.61.jar", "57b8e4bdb3a49163c3a6a501929240531523c111ff53844c9f82efba60f789f7"},
	{"tomcat/tomcat-annotations-api-10.1.61.jar", "8d408d675d89da99f86171ead3067f9e947accf722e3785e6775e9af387ff3a6"},
}

// WorkloadLibsBuild builds workload-libs.jar, the third-party HTTP
// module: its classes and the ServiceLoader entry registering its
// scenarios with the core workload's Main. Like workload-otel.jar it
// holds no dependency; its manifest's Class-Path names workload.jar and
// the vendored jars by their repo-relative paths, so `java -jar
// workload-libs.jar http-servlet …` runs from the repo root.
func WorkloadLibsBuild() error {
	mg.Deps(WorkloadBuild)
	if err := verifyJars(libsVendorDir, libsJars); err != nil {
		return err
	}
	srcs, err := javaSources(libsSrcDir)
	if err != nil {
		return err
	}
	// A clean class dir keeps classes of deleted sources out of the jar.
	if err := os.RemoveAll(libsClassesDir); err != nil {
		return err
	}
	classPath := libsClassPath()
	args := append([]string{"--release", workloadRelease, "-Xlint:all", "-Werror",
		"-cp", strings.Join(classPath, string(os.PathListSeparator)),
		"-d", libsClassesDir}, srcs...)
	if err := sh.RunV("javac", args...); err != nil {
		return err
	}
	return packModuleJar(libsJar, libsClassesDir, libsResDir, classPath)
}

// libsClassPath is workload.jar and the vendored jars, repo-relative.
func libsClassPath() []string {
	cp := []string{workloadJar}
	for _, lib := range libsJars {
		cp = append(cp, filepath.Join(libsVendorDir, lib.path))
	}
	return cp
}

// verifyJars checks each vendored jar under dir against its pinned
// SHA-256, so a swapped or corrupted copy fails the build instead of
// being run.
func verifyJars(dir string, jars []vendoredJar) error {
	for _, lib := range jars {
		path := filepath.Join(dir, lib.path)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != lib.sha256 {
			return fmt.Errorf("%s: sha256 %s, want %s (see its PROVENANCE)", path, got, lib.sha256)
		}
	}
	return nil
}

// packModuleJar writes a workload module's jar: Main-Class is the core
// workload's Main (the module's scenarios join its registry through
// META-INF/services), Class-Path the jars it runs against, relative to
// the jar's own directory.
func packModuleJar(jar, classes, resources string, classPath []string) error {
	manifest := filepath.Join(classes, "MANIFEST.MF")
	err := os.WriteFile(manifest, []byte("Main-Class: "+workloadMainClass+"\n"+
		"Class-Path: "+strings.Join(classPath, " ")+"\n"), 0o644)
	if err != nil {
		return err
	}
	return sh.RunV("jar", "--create", "--file", jar, "--manifest", manifest,
		"-C", classes, "iormesh",
		"-C", resources, ".")
}

// libsScenarios are workload-libs.jar's scenarios, one per hooked
// library (decision #22's order).
var libsScenarios = []string{"http-servlet"}

// checkLibsScenarios runs workload-libs.jar's scenarios (task de4) the
// way checkOtelScenario runs the OTEL flavor: each self-test, --list and
// --expect through the core Main (and workload.jar alone not knowing
// them), the two-process pair, the usage errors, and a client with
// nobody listening (exit 1).
func checkLibsScenarios() error {
	for _, name := range libsScenarios {
		if err := sh.RunV("java", "-jar", libsJar, "--self-test", name); err != nil {
			return err
		}
		if err := checkLibsListed(name); err != nil {
			return err
		}
		if err := checkTwoRolesJar(libsJar, name, nil, portClient); err != nil {
			return err
		}
		if err := checkLibsFailures(name); err != nil {
			return err
		}
	}
	return nil
}

// checkLibsFailures: client/server only, the client needs a --port, no
// --dir (exit 2); a client with nobody listening fails (exit 1).
func checkLibsFailures(name string) error {
	usage := [][]string{
		{name, "--role", "solo"},
		{name, "--role", "client"},
		{name, "--role", "server", "--dir", "x"},
	}
	for _, args := range usage {
		if err := expectExitJar(libsJar, nil, 2, args...); err != nil {
			return err
		}
	}
	port, err := closedPort()
	if err != nil {
		return err
	}
	return expectExitJar(libsJar, nil, 1, name, "--role", "client", "--port", fmt.Sprint(port))
}

// checkLibsListed: the jar's --list names the scenario beside the core
// ones, --expect prints its golden bundled in workload.jar, and
// workload.jar alone, without the libraries, does not know it.
func checkLibsListed(name string) error {
	out, err := runBoundedJar(libsJar, nil, "--list")
	if err != nil {
		return fmt.Errorf("%s --list: %w", libsJar, err)
	}
	names := strings.Fields(string(out))
	if !slices.Contains(names, name) || !slices.Contains(names, "http-plain") {
		return fmt.Errorf("%s --list: want %s and the core scenarios, got %q", libsJar, name, names)
	}
	exp, err := runBoundedJar(libsJar, nil, "--expect", name)
	if err != nil || !strings.Contains(string(exp), `"scenario": "`+name+`"`) {
		return fmt.Errorf("%s --expect %s: %q, %v", libsJar, name, exp, err)
	}
	return expectExit(2, name, "--role", "server")
}
