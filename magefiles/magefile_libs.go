//go:build mage

// The third-party HTTP workload module (task de4, docs/decisions.md
// #22's Servlet, Apache HttpClient, OkHttp and Netty): workload/libs, built
// into its own jar like the OTEL flavor (magefile_otel.go), so the core
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
	libsJar        = buildDir + "/workload-libs.jar"
	libsVendorDir  = "workload/libs/third_party"
)

// vendoredJar is one third-party jar: its path below a vendor directory
// and its SHA-256 (also in that library's PROVENANCE).
type vendoredJar struct{ path, sha256 string }

// libsJars are workload/libs's vendored jars: Tomcat's embeddable core
// (the Servlet API and container) with the annotations API it depends
// on; Apache HttpClient 5 with httpcore5 (and its HTTP/2 module) and the
// SLF4J API, its Maven dependencies; OkHttp 5 with Okio and the Kotlin
// standard library; Netty's HTTP codec with the modules it needs.
var libsJars = []vendoredJar{
	{"tomcat/tomcat-embed-core-10.1.61.jar", "57b8e4bdb3a49163c3a6a501929240531523c111ff53844c9f82efba60f789f7"},
	{"tomcat/tomcat-annotations-api-10.1.61.jar", "8d408d675d89da99f86171ead3067f9e947accf722e3785e6775e9af387ff3a6"},
	{"httpclient5/httpclient5-5.6.5.jar", "a10b555f7c85a720d1011a086d679d854b155816cde91a40198cc98c2b0867e8"},
	{"httpclient5/httpcore5-5.4.4.jar", "b08e3c3a440fec2029e89980134c7c787dd6796454641ad1eafb81478d7e8b0b"},
	{"httpclient5/httpcore5-h2-5.4.4.jar", "9a61441889f9b222ce99581977cc549b4dfca7ac9d2841582c1e57a31fa65b48"},
	{"httpclient5/slf4j-api-1.7.36.jar", "d3ef575e3e4979678dc01bf1dcce51021493b4d11fb7f1be8ad982877c16a1c0"},
	{"okhttp/okhttp-jvm-5.5.1.jar", "4039527bd1f2e40b66e20b0e52c689cf56074b938ea02678141495f5160f1047"},
	{"okhttp/okio-jvm-3.18.1.jar", "b97b640557a650d15411f2be29aef558c4e422a83653cb18ba494311dbed18be"},
	{"okhttp/kotlin-stdlib-2.1.21.jar", "263bdc679e1f62012db7b091796279b6d71cf36f4797a98ff1ace05835f201c8"},
	{"okhttp/annotations-13.0.jar", "ace2a10dc8e2d5fd34925ecac03e4988b2c0f851650c94b8cef49ba1bd111478"},
	{"netty/netty-codec-http-4.2.20.Final.jar", "d08b0d473c48c61ebb2bc4d145111fa1777832aee4d2be1024891e904e36eaf5"},
	{"netty/netty-codec-base-4.2.20.Final.jar", "5a66da26b86d7eecedd9e0d8fe0e22ef1733bbf873e476cb9b860afbed22210c"},
	{"netty/netty-codec-compression-4.2.20.Final.jar", "7ebae4ac80c01e2d279d0e50d377bcdaf0cf8f2f65797e0578afc810a66ab57d"},
	{"netty/netty-handler-4.2.20.Final.jar", "211dbbba074298046b37f841ad9ca89ed91968f6b9e97400c99ab4e6177b1f96"},
	{"netty/netty-transport-4.2.20.Final.jar", "ff4f1f8158c714c1b485ef10cf2456fbd3c1b2930256b70287aeb7dc3ad94783"},
	{"netty/netty-transport-native-unix-common-4.2.20.Final.jar", "6f600d5cbd3ce39e50680de66b00d7e6a9652dac7d08a5b022101d717cb7fb52"},
	{"netty/netty-resolver-4.2.20.Final.jar", "cb7a6027e4f366fb28dcea7e1f7088ca16bd23c2cc38e8ddfdac77a77bd144dc"},
	{"netty/netty-buffer-4.2.20.Final.jar", "794136fc6be78e44a286c2b1420a182e135c6d06eb8c397030c1551ee50acccd"},
	{"netty/netty-common-4.2.20.Final.jar", "5807fef7eb619bc0a5d1c0e6c83199ea9bca8503203a3eb73cfdccec2d215743"},
}

// WorkloadLibsBuild builds workload-libs.jar, the third-party HTTP
// module: its classes and the ServiceLoader entry registering its
// scenarios with the core workload's Main. Like workload-otel.jar it
// holds no dependency; its manifest's Class-Path names them relative to
// the jar's own directory, bin/ (manifestClassPath: workload.jar beside
// it, the vendored jars as ../workload/libs/third_party/…), so `java -jar
// bin/workload-libs.jar http-servlet …` runs from any directory.
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
// META-INF/services), Class-Path the jars it runs against. classPath is
// repo-relative (javac's -cp); the manifest wants each entry relative to
// the jar's own directory, so manifestClassPath rewrites it.
func packModuleJar(jar, classes, resources string, classPath []string) error {
	cp, err := manifestClassPath(jar, classPath)
	if err != nil {
		return err
	}
	manifest := filepath.Join(classes, "MANIFEST.MF")
	err = os.WriteFile(manifest, []byte(manifestHeader("Main-Class", workloadMainClass)+
		manifestHeader("Class-Path", cp)), 0o644)
	if err != nil {
		return err
	}
	if err := ensureBuildDir(); err != nil {
		return err
	}
	// Dated like the core workload jar (jarCreate, task bm4).
	date, err := sourceDate()
	if err != nil {
		return err
	}
	return jarCreate(date, "--file", jar, "--manifest", manifest,
		"-C", classes, "iormesh",
		"-C", resources, ".")
}

// manifestClassPath is classPath's repo-relative entries made relative to
// jar's directory and space-separated, slashes as the manifest wants: with
// the jars in bin/ (decision #30) workload.jar stays a bare name beside
// them and a vendored jar becomes ../workload/….
func manifestClassPath(jar string, classPath []string) (string, error) {
	rel := make([]string, 0, len(classPath))
	for _, p := range classPath {
		r, err := filepath.Rel(filepath.Dir(jar), p)
		if err != nil {
			return "", err
		}
		rel = append(rel, filepath.ToSlash(r))
	}
	return strings.Join(rel, " "), nil
}

// manifestHeader is one manifest header, folded as the jar format wants:
// no line longer than 72 bytes, each continuation line starting with a
// space (workload/libs's Class-Path names over a dozen jars).
func manifestHeader(name, value string) string {
	const maxLine = 72
	line := name + ": " + value
	var b strings.Builder
	for len(line) > maxLine {
		b.WriteString(line[:maxLine] + "\n")
		line = " " + line[maxLine:]
	}
	b.WriteString(line + "\n")
	return b.String()
}

// libsScenarios are workload-libs.jar's scenarios, one per hooked
// library (decision #22's order).
var libsScenarios = []string{"http-servlet", "http-apache", "http-okhttp", "http-netty"}

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
