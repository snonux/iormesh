package ctl

import (
	"errors"
	"fmt"
	"os"
	"time"

	"aead.dev/minisign"
	"golang.org/x/term"

	"iormesh/internal/release"
	"iormesh/internal/termsafe"
	"iormesh/internal/version"
)

// signPasswordEnv may carry the secret key's password for unattended
// runs, the variable `mage sign` reads too; without it the password is
// read from the terminal.
const signPasswordEnv = "IORMESH_MINISIGN_PASSWORD"

// signCommand signs an unpacked release archive with the operator's own
// minisign key (decision #32): the archives ship unsigned, and deploy and
// bootstrap verify against the key the operator pinned (decision #28), so
// each operator signs the builds they deploy. It signs only the release
// artifacts in DIR (release.SignRelease, the code `mage sign` runs too),
// each as its own name at -version, which must be every artifact's own
// stamp, then checks each signature with the key's public half. The
// password is never printed: it comes from $IORMESH_MINISIGN_PASSWORD or
// is read without echo from /dev/tty.
func signCommand(args []string, e env) error {
	fs := newFlagSet("sign", e.stderr)
	keyPath := fs.String("key", "", "your minisign secret key file (iormeshctl keygen or minisign -G; mode 0600, password-encrypted)")
	ver := fs.String("version", version.Version, "release version every artifact must be stamped with")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if *keyPath == "" || fs.NArg() != 1 {
		return fmt.Errorf("%w: sign takes -key FILE and one DIR", errUsage)
	}
	if _, err := version.Parse(*ver); err != nil {
		return fmt.Errorf("%w: -version: %w", errUsage, err)
	}
	password, err := signPassword(e)
	if err != nil {
		return err
	}
	key, err := release.ReadSecretKey(*keyPath, password, os.Getuid())
	if err != nil {
		return err
	}
	signed, err := release.SignRelease(key, fs.Arg(0), *ver, time.Now())
	if err != nil {
		return err
	}
	return checkSigned(e, key, signed)
}

// checkSigned verifies every signature just written with the key's public
// half, as deploy will with the pinned copy, and reports each file.
func checkSigned(e env, key minisign.PrivateKey, signed []string) error {
	pub, ok := key.Public().(minisign.PublicKey)
	if !ok {
		return errors.New("sign: key has no minisign public half")
	}
	for _, path := range signed {
		m, err := release.VerifyFile(pub, path)
		if err != nil {
			return fmt.Errorf("sign: own signature does not verify: %w", err)
		}
		_, _ = fmt.Fprintf(e.stdout, "%s: signed %s %s\n", termsafe.String(path),
			termsafe.String(m.File), termsafe.String(m.Version))
	}
	// Deploy and bootstrap accept them where this key's public half is
	// pinned; the id lets the operator compare with `minisign.pub`.
	_, _ = fmt.Fprintf(e.stdout, "%d files signed by key %X\n", len(signed), key.ID())
	return nil
}

// signPassword is $IORMESH_MINISIGN_PASSWORD, or read without echo from
// the controlling terminal; the prompt goes to stderr and the password
// nowhere.
func signPassword(e env) (string, error) {
	if p, ok := os.LookupEnv(signPasswordEnv); ok {
		return p, nil
	}
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return "", fmt.Errorf("sign: no terminal for the key password; set %s: %w", signPasswordEnv, err)
	}
	defer func() { _ = tty.Close() }()
	_, _ = fmt.Fprint(e.stderr, "minisign key password: ")
	p, err := term.ReadPassword(int(tty.Fd()))
	_, _ = fmt.Fprintln(e.stderr)
	if err != nil {
		return "", fmt.Errorf("sign: password: %w", err)
	}
	return string(p), nil
}
