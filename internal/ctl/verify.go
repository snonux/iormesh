package ctl

import (
	"fmt"
	"os"
	"path/filepath"

	"iormesh/internal/federate"
	"iormesh/internal/release"
)

// keyFileName is the pinned release key in the controller's config
// directory, next to servers.yaml (decision #28: the key is pinned in the
// controller config; the server's root-owned copy is
// /opt/iormesh/etc/minisign.pub).
const keyFileName = "minisign.pub"

// defaultKeyPath is ~/.config/iormesh/minisign.pub, in the registry's
// directory so both follow $XDG_CONFIG_HOME alike.
func defaultKeyPath() (string, error) {
	reg, err := federate.DefaultRegistryPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(reg), keyFileName), nil
}

// verifyCommand checks release binaries against their FILE.minisig with
// the pinned key — the check deploy runs before it pushes a tracer
// (decision #28), available on its own so an operator can vet dist/ or a
// downloaded build. The key must be the operator's own file, writable by
// nobody else: whoever could swap it could have any build verified. Every
// FILE is checked; one failure fails the command.
func verifyCommand(args []string, e env) error {
	fs := newFlagSet("verify", e.stderr)
	keyPath := fs.String("key", "", "pinned minisign public key (default ~/.config/iormesh/"+keyFileName+")")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%w: verify takes one or more FILEs", errUsage)
	}
	if *keyPath == "" {
		var err error
		if *keyPath, err = e.releaseKey(); err != nil {
			return err
		}
	}
	key, err := release.ReadKeyFile(*keyPath, os.Getuid())
	if err != nil {
		return err
	}
	failed := 0
	for _, path := range fs.Args() {
		m, err := release.VerifyFile(key, path)
		if err != nil {
			_, _ = fmt.Fprintf(e.stderr, "iormeshctl: %s: FAILED: %v\n", path, err)
			failed++
			continue
		}
		_, _ = fmt.Fprintf(e.stdout, "%s: OK, signed %s %s\n", path, m.File, m.Version)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d files failed verification", failed, fs.NArg())
	}
	return nil
}
