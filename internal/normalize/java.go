package normalize

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"iormesh/internal/schema"
)

// jdkPackages are the class-name prefixes of the JDK's own code: the
// frames between a scenario's method and the native I/O call (stream
// and channel implementations, the NIO dispatchers, the file system
// provider), never what a golden means by the causing method.
var jdkPackages = []string{"java.", "javax.", "jdk.", "sun.", "com.sun."}

// CausingMethod names the Java method that caused an event, the rule
// ExpectedJava.Method is written against (settled by task 2b4): the
// innermost frame of java.stack whose class is outside the JDK's own
// packages, as "class#method" — file and line dropped. "" when the event
// has no Java attribution, a stack id is not in the method table, or
// every frame is the JDK's (I/O the JDK did on its own behalf).
func CausingMethod(e schema.Event, methods map[uint64]schema.Method) string {
	if e.Java == nil {
		return ""
	}
	for _, id := range e.Java.Stack {
		m, ok := methods[id]
		if !ok {
			return ""
		}
		if !isJDK(m.Class) {
			return m.Class + "#" + m.Method
		}
	}
	return ""
}

func isJDK(class string) bool {
	for _, p := range jdkPackages {
		if strings.HasPrefix(class, p) {
			return true
		}
	}
	return false
}

// javaMatch compares a java declaration (nil: nothing declared) with the
// event's attribution: the thread name when declared, and the causing
// method.
func javaMatch(want *ExpectedJava, e schema.Event, methods map[uint64]schema.Method) bool {
	if want == nil {
		return true
	}
	if e.Attribution != schema.AttributionJava || e.Java == nil ||
		(want.Thread != "" && want.Thread != e.Java.Thread) {
		return false
	}
	return CausingMethod(e, methods) == want.Method
}

// LoadMethods reads a trace directory's method table file (methods.ndjson)
// strictly, by id; a missing file is no table (nil), as in a run that
// joined no JVM.
func LoadMethods(path string) (map[uint64]schema.Method, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	rows, err := schema.ReadTable[schema.Method](f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make(map[uint64]schema.Method, len(rows))
	for _, m := range rows {
		out[m.MethodID] = m
	}
	return out, nil
}
