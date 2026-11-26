// Package release signs and verifies iormesh release binaries
// (docs/decisions.md #28): minisign signatures, made with
// aead.dev/minisign, whose trusted comment names the artifact and its version.
// It is cgo-free, so the controller (iormeshctl verify, and deploy after it)
// and the tracer (iormesh upgrade) share it. Signing is the operator's,
// with their own key (decision #32): `mage sign` over a dist/ built from
// source and `iormeshctl sign` over an unpacked release archive run the
// same code here (SignDir, SignRelease, ReadSecretKey).
//
// The trusted comment is signed along with the binary, which is why the
// metadata lives there and not in a file name: a deploying user who can
// stage any file cannot rename an older build into a newer one, nor the
// arm64 tracer — or iormeshctl — into the amd64 tracer. The file format is
// plain minisign, so `minisign -Vm FILE -p KEY` checks a signature too.
package release

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"aead.dev/minisign"

	"iormesh/internal/version"
)

// SigSuffix is appended to a binary's path for its detached signature,
// minisign's own convention.
const SigSuffix = ".minisig"

// Size bounds for what is read before it is verified: a staged file is the
// caller's, so nothing unbounded is read into memory as root.
const (
	// MaxBinarySize is far above a static tracer's tens of MiB.
	MaxBinarySize = 256 << 20
	// MaxSigSize and MaxKeySize hold a minisign signature or public key
	// file (a few hundred bytes) with room for long comments.
	MaxSigSize = 4 << 10
	MaxKeySize = 4 << 10
	// maxFileName bounds the artifact name in a trusted comment.
	maxFileName = 128
)

// Errors a caller can tell apart.
var (
	// ErrBadSignature is any verification failure: a malformed or forged
	// signature, a modified binary, or a signature by another key.
	ErrBadSignature = errors.New("signature verification failed")
	// ErrBadComment is a valid signature whose trusted comment does not
	// carry the file and version this package writes.
	ErrBadComment = errors.New("signed comment lacks iormesh release metadata")
)

// Meta is what a signature vouches for besides the bytes: the artifact's
// name (dist/'s file name, e.g. iormesh-linux-amd64) and its version.
type Meta struct {
	File, Version string
}

// TracerFile is the release name of the tracer for goarch — the one name
// `iormesh upgrade` installs on a host of that arch.
func TracerFile(goarch string) string {
	return "iormesh-linux-" + goarch
}

// AgentFile is the release name of the Java agent jar (`mage agentBuild`
// copies it into dist/, `mage sign` signs it): architecture-neutral, so one
// name serves every host. `iormesh upgrade` installs a file signed with
// this name as the host's agent jar, never as the tracer.
const AgentFile = "iormesh-agent.jar"

// Artifacts are the release names an archive packs for arch (decision
// #32): its static tracer, the architecture-neutral agent jar, and the
// controller for Linux and macOS on that arch. Deploy and bootstrap
// verify the tracer and the jar; the controllers are signed alike so an
// operator can check the one they install.
func Artifacts(arch string) []string {
	return []string{
		TracerFile(arch),
		AgentFile,
		"iormeshctl-linux-" + arch,
		"iormeshctl-darwin-" + arch,
	}
}

// IsArtifact reports whether name is a release artifact's name, of any
// arch (lowercase letters and digits): what SignRelease signs in an
// unpacked release, and nothing else there (LICENSE, SHA256SUMS).
func IsArtifact(name string) bool {
	if name == AgentFile {
		return true
	}
	for _, prefix := range []string{"iormesh-linux-", "iormeshctl-linux-", "iormeshctl-darwin-"} {
		if arch, ok := strings.CutPrefix(name, prefix); ok {
			return arch != "" && !strings.ContainsFunc(arch, func(r rune) bool {
				return (r < 'a' || r > 'z') && (r < '0' || r > '9')
			})
		}
	}
	return false
}

// ParsePublicKey reads a minisign public key file (an untrusted comment
// line, then the base64 key) or the bare base64 key.
func ParsePublicKey(data []byte) (minisign.PublicKey, error) {
	var key minisign.PublicKey
	if err := key.UnmarshalText(bytes.TrimSpace(data)); err != nil {
		return minisign.PublicKey{}, fmt.Errorf("public key: %w", err)
	}
	return key, nil
}

// Sign signs data as the artifact m, with now as the signed timestamp. The
// signature is minisign's prehashed form (BLAKE2b-512, then Ed25519), the
// default of minisign itself.
func Sign(key minisign.PrivateKey, data []byte, m Meta, now time.Time) ([]byte, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	// minisign.Reader hashes what is read through it; draining it once
	// feeds the whole binary to the prehash.
	r := minisign.NewReader(bytes.NewReader(data))
	if _, err := io.Copy(io.Discard, r); err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	untrusted := "iormesh release signature, key " + strings.ToUpper(strconv.FormatUint(key.ID(), 16))
	return r.SignWithComments(key, m.comment(now), untrusted), nil
}

// Verify checks sig over data with the pinned key and returns the signed
// metadata. Every failure wraps ErrBadSignature or ErrBadComment; nothing
// in an unverified signature is trusted, its key id only names the
// mismatch.
func Verify(key minisign.PublicKey, data, sig []byte) (Meta, error) {
	var s minisign.Signature
	if err := s.UnmarshalText(sig); err != nil {
		return Meta{}, fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	if s.KeyID != key.ID() {
		return Meta{}, fmt.Errorf("%w: signed by key %X, the pinned key is %X", ErrBadSignature, s.KeyID, key.ID())
	}
	// minisign.Verify takes the whole message and handles both the
	// prehashed and the legacy algorithm; it also checks the trusted
	// comment's own signature, so the comment parsed below is authentic.
	if !minisign.Verify(key, data, sig) {
		return Meta{}, fmt.Errorf("%w: the file does not match its signature", ErrBadSignature)
	}
	return parseComment(s.TrustedComment)
}

// comment is m as a trusted comment, in minisign's tab-separated
// key:value style ("timestamp:… file:…" is minisign's own default).
func (m Meta) comment(now time.Time) string {
	return fmt.Sprintf("timestamp:%d\tfile:%s\tversion:%s", now.Unix(), m.File, m.Version)
}

// validate requires a plain file name and a parseable version, so the
// comment round-trips through parseComment.
func (m Meta) validate() error {
	if m.File == "" || len(m.File) > maxFileName || strings.ContainsAny(m.File, "/\\:") ||
		strings.IndexFunc(m.File, isSpaceOrControl) >= 0 {
		return fmt.Errorf("%w: bad file name %q", ErrBadComment, m.File)
	}
	if _, err := version.Parse(m.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrBadComment, err)
	}
	return nil
}

// parseComment reads file: and version: from a trusted comment; other
// fields (timestamp, minisign's "hashed") are ignored, a repeated field is
// refused so the comment has one reading only.
func parseComment(c string) (Meta, error) {
	var m Meta
	seen := map[string]bool{}
	for _, f := range strings.Fields(c) {
		k, v, ok := strings.Cut(f, ":")
		if !ok || (k != "file" && k != "version") {
			continue
		}
		if seen[k] {
			return Meta{}, fmt.Errorf("%w: %s: appears twice", ErrBadComment, k)
		}
		seen[k] = true
		if k == "file" {
			m.File = v
		} else {
			m.Version = v
		}
	}
	if err := m.validate(); err != nil {
		return Meta{}, fmt.Errorf("trusted comment %q: %w", c, err)
	}
	return m, nil
}

func isSpaceOrControl(r rune) bool {
	return r <= ' ' || r == 0x7f
}
