package ctl

import (
	"fmt"
	"os"

	"iormesh/internal/release"
	"iormesh/internal/termsafe"
)

// keygenCommand makes the operator's minisign key pair without a terminal
// (decision #38): an agent or a script that holds the password in
// $IORMESH_MINISIGN_PASSWORD, the variable sign reads, makes the key sign
// then reads with that same password. The secret key goes to -key, the
// public key to -pub (default the pinned key path verify, deploy and
// bootstrap read, ~/.config/iormesh/minisign.pub); release.WriteKeyPair
// writes both new and 0600, makes missing directories 0700 and never
// replaces a file. The password comes only from the environment: a human
// at a terminal has `minisign -G`, which asks twice, and an unset or empty
// variable is refused rather than prompted for or used for an unencrypted
// key. Neither the password nor the secret key is ever printed; the
// output names the two files and the key id.
func keygenCommand(args []string, e env) error {
	fs := newFlagSet("keygen", e.stderr)
	keyPath := fs.String("key", "", "the new minisign secret key file (created 0600; must not exist)")
	pubPath := fs.String("pub", "", "the new public key file (default the pinned key, ~/.config/iormesh/minisign.pub)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if *keyPath == "" || fs.NArg() != 0 {
		return fmt.Errorf("%w: keygen takes -key FILE and no arguments", errUsage)
	}
	if *pubPath == "" {
		p, err := e.releaseKey()
		if err != nil {
			return err
		}
		*pubPath = p
	}
	password, ok := os.LookupEnv(signPasswordEnv)
	if !ok {
		return fmt.Errorf("keygen: set %s to the new key's password (by hand, minisign -G asks on the terminal)",
			signPasswordEnv)
	}
	if password == "" {
		return fmt.Errorf("keygen: %s is empty; the secret key must be password-encrypted", signPasswordEnv)
	}
	pub, err := release.WriteKeyPair(*keyPath, *pubPath, password, os.Getuid())
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.stdout, "secret key: %s\npublic key: %s\nkey id: %X\n",
		termsafe.String(*keyPath), termsafe.String(*pubPath), pub.ID())
	return nil
}
