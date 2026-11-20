//go:build mage

// Release signing (PLAN.md decision #28, docs/build.md "Signing"): Sign
// writes a minisign signature next to every binary the container builds
// left in dist/. The private key is the release pipeline's and never
// enters the repo: it is read from the path IORMESH_MINISIGN_KEY names,
// which must lie outside the tree, and stays encrypted at rest (minisign's
// scrypt-encrypted key file).

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"aead.dev/minisign"
	"golang.org/x/term"

	"iormesh/internal/release"
	"iormesh/internal/version"
)

const (
	// signKeyEnv names the minisign secret key file (`minisign -G` makes
	// one); signPasswordEnv may carry its password for unattended runs,
	// otherwise it is read from the terminal.
	signKeyEnv      = "IORMESH_MINISIGN_KEY"
	signPasswordEnv = "IORMESH_MINISIGN_PASSWORD"
)

// Sign signs every binary in dist/ as its file name at internal/version's
// Version, writing NAME.minisig, then verifies each signature with the
// key's public half. The key comes from $IORMESH_MINISIGN_KEY (outside
// the repository), its password from $IORMESH_MINISIGN_PASSWORD or the
// terminal.
func Sign() error {
	key, err := loadSigningKey()
	if err != nil {
		return err
	}
	signed, err := release.SignDir(key, "dist", version.Version, time.Now())
	if err != nil {
		return err
	}
	if len(signed) == 0 {
		return errors.New("sign: dist/ has no binaries; run mage buildPodman or buildDocker first")
	}
	pub, ok := key.Public().(minisign.PublicKey)
	if !ok {
		return errors.New("sign: key has no minisign public half")
	}
	for _, path := range signed {
		m, err := release.VerifyFile(pub, path)
		if err != nil {
			return fmt.Errorf("sign: own signature does not verify: %w", err)
		}
		fmt.Printf("signed %s as %s %s\n", path, m.File, m.Version)
	}
	return nil
}

// loadSigningKey reads and decrypts the key $IORMESH_MINISIGN_KEY names,
// refusing one inside this tree: a key there is one `git add -A` away
// from the repository.
func loadSigningKey() (minisign.PrivateKey, error) {
	path := os.Getenv(signKeyEnv)
	if path == "" {
		return minisign.PrivateKey{}, fmt.Errorf("sign: set %s to the minisign secret key file", signKeyEnv)
	}
	inside, err := insideRepo(path)
	if err != nil {
		return minisign.PrivateKey{}, fmt.Errorf("sign: %s: %w", path, err)
	}
	if inside {
		return minisign.PrivateKey{}, fmt.Errorf("sign: %s must lie outside the repository", path)
	}
	password, err := signingPassword()
	if err != nil {
		return minisign.PrivateKey{}, err
	}
	key, err := minisign.PrivateKeyFromFile(password, path)
	if err != nil {
		return minisign.PrivateKey{}, fmt.Errorf("sign: %s: %w", path, err)
	}
	return key, nil
}

// insideRepo reports whether path resolves into the working directory,
// which mage runs from: the repository root.
func insideRepo(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	wd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	if resolved, err := filepath.EvalSymlinks(wd); err == nil {
		wd = resolved
	}
	// IsLocal, not a ".." prefix test: "..key" is a local name, and an
	// absolute rel (another volume) is not inside.
	rel, err := filepath.Rel(wd, abs)
	return err == nil && filepath.IsLocal(rel), nil
}

// signingPassword is $IORMESH_MINISIGN_PASSWORD, or read without echo
// from the controlling terminal.
func signingPassword() (string, error) {
	if p, ok := os.LookupEnv(signPasswordEnv); ok {
		return p, nil
	}
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return "", fmt.Errorf("sign: no terminal for the key password; set %s: %w", signPasswordEnv, err)
	}
	defer func() { _ = tty.Close() }()
	fmt.Fprint(os.Stderr, "minisign key password: ")
	p, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("sign: password: %w", err)
	}
	return string(p), nil
}
