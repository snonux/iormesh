package javaattach

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sigquitBit is SIGQUIT's bit in /proc/<pid>/status's signal masks
// (signal 3, bit 3-1).
const sigquitBit = 1 << 2

// disableAttachFlag is the HotSpot option that turns the attach listener
// off; SIGQUIT then only prints a thread dump.
const disableAttachFlag = "-XX:+DisableAttachMechanism"

// toolOptionVars are the environment variables a JVM reads options from
// besides its command line (JDK_JAVA_OPTIONS only through the launcher).
var toolOptionVars = []string{"JAVA_TOOL_OPTIONS=", "JDK_JAVA_OPTIONS="}

// procInfo is what the handshake needs of the target process.
type procInfo struct {
	// euid is the owner the attach socket must have.
	euid uint32
	// nspid is the pid inside the JVM's own pid namespace: the number in
	// its socket's and trigger file's names.
	nspid uint32
	// quitCaught is whether a SIGQUIT handler is installed (SigCgt).
	quitCaught bool
}

// readProcInfo parses /proc/<pid>/status under root.
func readProcInfo(root string, pid uint32) (procInfo, error) {
	data, err := os.ReadFile(procPath(root, pid, "status"))
	if err != nil {
		return procInfo{}, err
	}
	return parseStatus(data)
}

// parseStatus reads Uid's effective field, NSpid's innermost pid and
// SigCgt's SIGQUIT bit from a status file. A kernel without NSpid (before
// 4.1) has one pid namespace's view: the Pid line.
func parseStatus(data []byte) (procInfo, error) {
	var info procInfo
	var haveUID, haveCgt bool
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		var err error
		switch key {
		case "Uid":
			// real, effective, saved, filesystem
			if len(fields) < 2 {
				return procInfo{}, fmt.Errorf("status: short Uid line %q", value)
			}
			info.euid, err = parseUint32(fields[1])
			haveUID = true
		case "Pid":
			if info.nspid == 0 {
				info.nspid, err = parseUint32(fields[0])
			}
		case "NSpid":
			info.nspid, err = parseUint32(fields[len(fields)-1])
		case "SigCgt":
			var mask uint64
			mask, err = strconv.ParseUint(fields[0], 16, 64)
			info.quitCaught, haveCgt = mask&sigquitBit != 0, true
		}
		if err != nil {
			return procInfo{}, fmt.Errorf("status %s: %w", key, err)
		}
	}
	if !haveUID || !haveCgt || info.nspid == 0 {
		return procInfo{}, errors.New("status: no Uid, NSpid or SigCgt line")
	}
	return info, sc.Err()
}

func parseUint32(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	return uint32(n), err
}

// mapsJVM reports whether the process maps HotSpot's libjvm.so: the
// attach protocol is HotSpot's, and only a JVM survives its SIGQUIT.
func mapsJVM(root string, pid uint32) (bool, error) {
	f, err := os.Open(procPath(root, pid, "maps"))
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	return parseMaps(f)
}

// parseMaps reports whether a maps file names libjvm.so.
func parseMaps(r io.Reader) (bool, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// address perms offset dev inode path
		if len(fields) == 6 && filepath.Base(fields[5]) == "libjvm.so" {
			return true, nil
		}
	}
	return false, sc.Err()
}

// attachDisabled reports whether the JVM was started with
// -XX:+DisableAttachMechanism, on its command line or in an options
// variable. Best effort: an @argfile or a -XX:Flags file is not read, and
// an unreadable environ (another uid, the caller not root) counts as
// "not disabled" — the JVM then answers the SIGQUIT with a thread dump
// and the socket wait times out.
func attachDisabled(root string, pid uint32) bool {
	if cmdline, err := os.ReadFile(procPath(root, pid, "cmdline")); err == nil {
		for _, arg := range bytes.Split(cmdline, []byte{0}) {
			if string(arg) == disableAttachFlag {
				return true
			}
		}
	}
	environ, err := os.ReadFile(procPath(root, pid, "environ"))
	if err != nil {
		return false
	}
	for _, kv := range bytes.Split(environ, []byte{0}) {
		for _, v := range toolOptionVars {
			if value, ok := strings.CutPrefix(string(kv), v); ok &&
				containsField(value, disableAttachFlag) {
				return true
			}
		}
	}
	return false
}

func containsField(s, field string) bool {
	for _, f := range strings.Fields(s) {
		if f == field {
			return true
		}
	}
	return false
}

func procPath(root string, pid uint32, name string) string {
	return filepath.Join(root, strconv.FormatUint(uint64(pid), 10), name)
}
