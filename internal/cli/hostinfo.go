package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"iormesh/internal/k8s"
	"iormesh/internal/schema"
)

// fqdnTimeout bounds the canonical-name lookup: a host with a broken
// resolver must not delay the trace start by the resolver's own timeout.
const fqdnTimeout = time.Second

// machineIDPath is where a host's id is read; a variable so a test can
// point it elsewhere.
var machineIDPath = "/etc/machine-id"

// describeHost is the host identity every event carries (host_id) and
// the session header's host description. It runs before the trace
// directory is touched, so a host it cannot describe leaves an old trace
// intact, like a tracer that cannot attach.
//
// In a pod (the downward-API variables set, k8s.PodFromEnv) the host is
// the pod (PLAN.md M7, task 7f4): host_id is "pod-<uid>" and the FQDN
// <name>.<namespace>.<uid[:8]>, and neither /etc/machine-id nor the
// resolver is consulted — even when the image carries a machine-id,
// which would be the image's, not the node's or the pod's. A partial or
// malformed pod environment is an error, never a fallback to the
// machine-id. Outside a pod: /etc/machine-id and the canonical hostname.
func describeHost(ctx context.Context, getenv func(string) string) (string, schema.Session, error) {
	pod, inPod, err := k8s.PodFromEnv(getenv)
	if err != nil {
		return "", schema.Session{}, err
	}
	s, err := kernelSession()
	if err != nil {
		return "", schema.Session{}, err
	}
	var hostID string
	if inPod {
		hostID, s.FQDN = pod.HostID(), pod.FQDN()
	} else {
		if hostID, err = machineID(); err != nil {
			return "", schema.Session{}, err
		}
		host, err := os.Hostname()
		if err != nil {
			return "", schema.Session{}, fmt.Errorf("hostname: %w", err)
		}
		s.FQDN = canonicalName(ctx, host)
	}
	if s.FQDN == "" {
		return "", schema.Session{}, fmt.Errorf("host description incomplete: %+v", s)
	}
	return hostID, s, nil
}

// machineID is a host's id outside a pod (/etc/machine-id).
func machineID() (string, error) {
	b, err := os.ReadFile(machineIDPath)
	if err != nil {
		return "", fmt.Errorf("host id: %w", err)
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", fmt.Errorf("host id: %s is empty", machineIDPath)
	}
	return id, nil
}

// kernelSession is the part of the session header the kernel answers, in
// a pod as on a host: the boot id (shared by every pod of a node, so it
// groups pods by node), the kernel release and the architecture.
func kernelSession() (schema.Session, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return schema.Session{}, fmt.Errorf("boot id: %w", err)
	}
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return schema.Session{}, fmt.Errorf("uname: %w", err)
	}
	s := schema.Session{
		BootID: strings.TrimSpace(string(b)),
		Kernel: unix.ByteSliceToString(uts.Release[:]),
		Arch:   unix.ByteSliceToString(uts.Machine[:]),
	}
	if s.BootID == "" || s.Kernel == "" || s.Arch == "" {
		return schema.Session{}, errors.New("host description incomplete: no boot id, kernel or arch")
	}
	return s, nil
}

// canonicalName is host's canonical name (what `hostname -f` prints), or
// host itself when the resolver knows no dotted name for it: the header
// must name the host either way, and a short name is still a name.
func canonicalName(ctx context.Context, host string) string {
	if strings.Contains(host, ".") {
		return host
	}
	ctx, cancel := context.WithTimeout(ctx, fqdnTimeout)
	defer cancel()
	cname, err := net.DefaultResolver.LookupCNAME(ctx, host)
	cname = strings.TrimSuffix(cname, ".")
	if err != nil || !strings.Contains(cname, ".") {
		return host
	}
	return cname
}
