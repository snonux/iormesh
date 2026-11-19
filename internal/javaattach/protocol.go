package javaattach

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// protocolVersion is the attach protocol every HotSpot from 6 on accepts,
// the one jdk.attach itself sends for a load.
const protocolVersion = "1"

// maxReply bounds what is read of the JVM's reply: a load's is one or
// two short lines, and a confused peer must not fill the tracer's memory.
const maxReply = 64 << 10

// returnCodePrefix starts a load's result line since JDK 9; 0 means the
// agent's Agent_OnAttach (the instrument library running agentmain)
// succeeded.
const returnCodePrefix = "return code: "

// dynamicLoadingOff is the start of HotSpot's reply when the JVM runs
// with -XX:-EnableDynamicAgentLoading (JEP 451; the default in a later
// release): the load is refused and the JVM keeps running.
const dynamicLoadingOff = "Dynamic agent loading is not enabled"

// ErrDynamicLoadingDisabled: the JVM refuses dynamically loaded agents
// (-XX:-EnableDynamicAgentLoading, or a future default); it must be
// started with -XX:+EnableDynamicAgentLoading or with -javaagent.
var ErrDynamicLoadingDisabled = errors.New("dynamic agent loading is disabled in the target JVM" +
	" (start it with -XX:+EnableDynamicAgentLoading, or use -javaagent)")

// loadRequest is protocol 1's request to load a Java agent: the version,
// the command and its three arguments, each NUL-terminated. "instrument"
// is the JDK library that runs agentmain; "false" says the library name
// is not a path; the third argument is "<jar>[=<options>]".
func loadRequest(jar, options string) []byte {
	arg := jar
	if options != "" {
		arg += "=" + options
	}
	var b bytes.Buffer
	for _, s := range []string{protocolVersion, "load", "instrument", "false", arg} {
		b.WriteString(s)
		b.WriteByte(0)
	}
	return b.Bytes()
}

// readReply reads the JVM's reply to EOF (the JVM closes the connection
// after one command), at most maxReply bytes.
func readReply(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxReply))
	return string(data), err
}

// parseLoadReply judges a load's reply: the completion status line (0:
// the command ran), then the load's result — "return code: N" (JDK 9 on)
// or a bare N (JDK 8), where 0 is success. Anything else is the JVM's
// own message saying why the agent was not loaded.
func parseLoadReply(reply string) error {
	status, rest, _ := strings.Cut(reply, "\n")
	code, err := strconv.Atoi(strings.TrimSpace(status))
	if err != nil {
		return fmt.Errorf("attach: unexpected reply %s", truncate(reply))
	}
	rest = strings.TrimSpace(rest)
	// JDK 21 to 27-ea answer status 0 and the message; matched before
	// the status so a release that turns it into a failed status still
	// gets the actionable error.
	if strings.HasPrefix(rest, dynamicLoadingOff) {
		return fmt.Errorf("%w: %s", ErrDynamicLoadingDisabled, truncate(rest))
	}
	if code != 0 {
		return fmt.Errorf("attach: the JVM refused the load (status %d): %s", code, truncate(rest))
	}
	result := strings.TrimPrefix(rest, returnCodePrefix)
	n, err := strconv.Atoi(result)
	switch {
	case rest == "":
		return errors.New("attach: the JVM sent no load result")
	case err != nil:
		return fmt.Errorf("attach: agent not loaded: %s", truncate(rest))
	case n != 0:
		return fmt.Errorf("attach: agent failed to start (return code %d)", n)
	}
	return nil
}

// truncate keeps an error message to its first line of reasonable
// length: the reply comes from another process.
func truncate(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	const limit = 300
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return strconv.Quote(s)
}
