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

	"iormesh/internal/schema"
)

// fqdnTimeout bounds the canonical-name lookup: a host with a broken
// resolver must not delay the trace start by the resolver's own timeout.
const fqdnTimeout = time.Second

// machineID is the host id every event carries (/etc/machine-id).
func machineID() (string, error) {
	b, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return "", fmt.Errorf("host id: %w", err)
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", errors.New("host id: /etc/machine-id is empty")
	}
	return id, nil
}

// hostSession gathers the session header's host description. It runs
// before the trace directory is touched, so a host it cannot describe
// leaves an old trace intact, like a tracer that cannot attach.
func hostSession(ctx context.Context) (schema.Session, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return schema.Session{}, fmt.Errorf("boot id: %w", err)
	}
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return schema.Session{}, fmt.Errorf("uname: %w", err)
	}
	host, err := os.Hostname()
	if err != nil {
		return schema.Session{}, fmt.Errorf("hostname: %w", err)
	}
	s := schema.Session{
		BootID: strings.TrimSpace(string(b)),
		FQDN:   canonicalName(ctx, host),
		Kernel: unix.ByteSliceToString(uts.Release[:]),
		Arch:   unix.ByteSliceToString(uts.Machine[:]),
	}
	if s.BootID == "" || s.FQDN == "" || s.Kernel == "" || s.Arch == "" {
		return schema.Session{}, fmt.Errorf("host description incomplete: %+v", s)
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
