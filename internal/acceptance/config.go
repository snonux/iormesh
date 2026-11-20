package acceptance

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"iormesh/internal/federate"
)

// Environment variables the run takes besides the two host arguments
// (docs/testing.md, "M4 acceptance on two real hosts", lists them).
const (
	EnvAddr1    = "IORMESH_M4_ADDR1"     // HOST1's IPv4 as HOST2 reaches it
	EnvAddr2    = "IORMESH_M4_ADDR2"     // HOST2's IPv4 as HOST1 reaches it
	EnvDist     = "IORMESH_M4_DIST"      // signed tracer builds (default dist)
	EnvKey      = "IORMESH_M4_KEY"       // pinned release key
	EnvNATPort  = "IORMESH_M4_NAT_PORT"  // HOST2's DNAT'ed port; unset: NAT skipped
	EnvFillDir  = "IORMESH_M4_FILL_DIR"  // watchdog fill directory on HOST2
	EnvPortBase = "IORMESH_M4_PORT_BASE" // first of the workload ports
	EnvRounds   = "IORMESH_M4_ROUNDS"    // streaming collect rounds
	EnvWorkDir  = "IORMESH_M4_WORKDIR"   // iormeshctl's private HOME
)

// Config is one acceptance run's inputs.
type Config struct {
	// Host1 and Host2 are registry host names ([user@]name, as ssh takes
	// them). Host1 runs the clients, the single-host scenarios and the
	// microservices; Host2 the servers and the watchdog project.
	Host1, Host2 string
	// Addr1 and Addr2 are the hosts' IPv4 addresses on the path between
	// them; "" resolves the name on the other host (getent).
	Addr1, Addr2 string
	// NATPort is the port HOST2 DNATs to the NAT server's port (the
	// operator's rule, see the runbook); 0 skips the NAT criterion.
	NATPort int
	// PortBase is the first workload port; the run uses PortBase+1 to
	// PortBase+13 (Ports).
	PortBase int
	// Dist holds iormesh-linux-<arch> and .minisig per host arch; Key is
	// the release public key deploy pins.
	Dist, Key string
	// Jar is the local workload.jar, Ctl the local iormeshctl binary,
	// Goldens the scenarios' directory (workload/scenarios) whose
	// expect.json the single-host check reads.
	Jar, Ctl, Goldens string
	// WorkDir is iormeshctl's HOME for the run: its registry, trace
	// store, service database and builds, kept afterwards.
	WorkDir string
	// FillDir is where the watchdog's fill file goes on HOST2; "" picks
	// the SSH user's home or /var/tmp, whichever shares the trace dir's
	// filesystem.
	FillDir string
	// Rounds of streaming collect (at least 2), each after Rotation.
	Rounds   int
	Rotation time.Duration
}

// Defaults that are not paths.
const (
	defaultPortBase = 18200
	defaultRounds   = 3
	// defaultRotation outlasts the tracer's one-minute segment rotation,
	// so every round finds the previous round's work published.
	defaultRotation = 65 * time.Second
)

// FromEnv builds the Config from the two host arguments and getenv.
// home is the operator's home directory (for the defaults). It refuses
// a missing, malformed or repeated host before anything runs.
func FromEnv(host1, host2, home string, getenv func(string) string) (Config, error) {
	c := Config{Host1: host1, Host2: host2, Addr1: getenv(EnvAddr1), Addr2: getenv(EnvAddr2),
		Dist: or(getenv(EnvDist), "dist"), FillDir: getenv(EnvFillDir),
		Key:      or(getenv(EnvKey), filepath.Join(home, ".config", "iormesh", "minisign.pub")),
		WorkDir:  or(getenv(EnvWorkDir), filepath.Join(home, ".iormesh-m4")),
		PortBase: defaultPortBase, Rounds: defaultRounds, Rotation: defaultRotation}
	var err error
	if c.NATPort, err = intEnv(getenv, EnvNATPort, 0, 1, 65535); err != nil {
		return c, err
	}
	if c.PortBase, err = intEnv(getenv, EnvPortBase, defaultPortBase, 1024, 65535-ports); err != nil {
		return c, err
	}
	if c.Rounds, err = intEnv(getenv, EnvRounds, defaultRounds, 2, 10); err != nil {
		return c, err
	}
	return c, c.validate()
}

// validate refuses what would make the run touch the wrong machine or
// misread its results.
func (c Config) validate() error {
	if c.Host1 == "" || c.Host2 == "" {
		return errors.New("m4Acceptance needs two hosts: mage m4Acceptance HOST1 HOST2")
	}
	for _, h := range []string{c.Host1, c.Host2} {
		if !federate.ValidHost(h) {
			return fmt.Errorf("host %q: want [user@]name from [A-Za-z0-9._-]", h)
		}
	}
	if strings.EqualFold(hostName(c.Host1), hostName(c.Host2)) {
		return fmt.Errorf("HOST1 and HOST2 are the same host (%s): the run needs two", c.Host1)
	}
	for _, a := range []string{c.Addr1, c.Addr2} {
		if a == "" {
			continue
		}
		if ip, err := netip.ParseAddr(a); err != nil || !ip.Is4() {
			return fmt.Errorf("address %q: want an IPv4 literal (the tcp-two-host scenario takes IPv4 only)", a)
		}
	}
	if c.NATPort != 0 && c.NATPort >= c.PortBase && c.NATPort <= c.PortBase+ports {
		return fmt.Errorf("%s %d collides with the workload ports %d-%d", EnvNATPort, c.NATPort,
			c.PortBase+1, c.PortBase+ports)
	}
	return nil
}

// hostName is a registry host without its user@ part.
func hostName(h string) string {
	if _, name, ok := strings.Cut(h, "@"); ok {
		return name
	}
	return h
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// intEnv reads an integer variable within [lo, hi]; unset is def.
func intEnv(getenv func(string) string, key string, def, lo, hi int) (int, error) {
	s := getenv(key)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s=%q: want an integer in [%d, %d]", key, s, lo, hi)
	}
	return n, nil
}
