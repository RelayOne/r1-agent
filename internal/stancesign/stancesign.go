// Package stancesign manages per-stance cryptographic signing
// identities for git commits. Each stance (cto, dev, reviewer, po,
// etc.) gets its own SSH signing key — never held by the executing
// model context — so a commit signed by "reviewer" cannot have been
// produced by the worker stance and vice versa. This closes the
// separation-of-concerns loop that the existing cross-model-review
// pattern opened: the cross-model reviewer is correct to not trust
// the writer, but without a cryptographic attestation of which
// stance authored a change, the reviewer can be fooled by a worker
// that mislabels its output.
//
// SSH signatures via `git commit -S` (git 2.34+) chosen over GPG
// because:
//
//   - No keyring configuration. The signing key is a plain ed25519
//     file in ~/.stoke/stance-keys/ per stance. git reads it via
//     user.signingkey; no gpg-agent, no expiry, no revocation dance.
//
//   - Verifiable out-of-band. Each stance's public key is its own
//     identity artifact; operators can pin expected keys in
//     stoke.policy.yaml without a full PKI.
//
//   - Faster than GPG: no subprocess warm-up per commit.
//
// Identities are lazily created on first use. Private keys are stored
// with 0600 permissions. The returned Identity carries the env-var
// block a caller should apply to an `exec.Cmd` invoking git.
package stancesign

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Identity is a resolved per-stance signing identity.
type Identity struct {
	Stance         string
	KeyPath        string
	PublicKeyPath  string
	AuthorName     string
	AuthorEmail    string
	CommitterName  string
	CommitterEmail string
}

// EnvVars returns the env-var slice the caller should pass to an
// `exec.Cmd` running git. Applying these produces a commit authored
// and committed-as this stance's identity, signed with this stance's
// private key.
func (id Identity) EnvVars() []string {
	return []string{
		"GIT_AUTHOR_NAME=" + id.AuthorName,
		"GIT_AUTHOR_EMAIL=" + id.AuthorEmail,
		"GIT_COMMITTER_NAME=" + id.CommitterName,
		"GIT_COMMITTER_EMAIL=" + id.CommitterEmail,
	}
}

// GitConfig returns the git config key/value pairs that should be
// applied (via `git -c key=value`) so a commit uses this stance's
// signing key. Combine with EnvVars for a full attribution.
func (id Identity) GitConfig() []string {
	return []string{
		"-c", "user.name=" + id.CommitterName,
		"-c", "user.email=" + id.CommitterEmail,
		"-c", "user.signingkey=" + id.PublicKeyPath,
		"-c", "gpg.format=ssh",
		"-c", "commit.gpgsign=true",
	}
}

// ApplyTo wires this identity into the given exec.Cmd: sets the
// environment + prepends git config flags. Expects cmd.Args[0] to
// already be "git" and cmd.Args[1:] to be the subcommand; inserts
// our config flags between them.
//
// Idempotent on a cmd that already has our flags: we check by
// the sentinel "user.signingkey=" prefix. Repeated application
// would produce a technically-valid-but-noisy command line.
func (id Identity) ApplyTo(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.Env = append(cmd.Env, id.EnvVars()...)
	// Insert git config flags after cmd path but before any existing args
	// that would be git's subcommand.
	if len(cmd.Args) == 0 {
		return
	}
	already := false
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "user.signingkey=") {
			already = true
			break
		}
	}
	if already {
		return
	}
	newArgs := []string{cmd.Args[0]}
	newArgs = append(newArgs, id.GitConfig()...)
	if len(cmd.Args) > 1 {
		newArgs = append(newArgs, cmd.Args[1:]...)
	}
	cmd.Args = newArgs
}

// IdentityFor returns a resolved Identity for the given stance,
// creating its signing key on disk if one does not already exist.
// Keys live under baseDir/<stance>/ (typically baseDir is
// $HOME/.stoke/stance-keys). Safe to call concurrently from multiple
// goroutines — creation is guarded by identityMu.
func IdentityFor(baseDir, stance string) (*Identity, error) {
	stance = sanitizeStance(stance)
	if stance == "" {
		return nil, fmt.Errorf("stancesign: empty stance name")
	}
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("stancesign: %w", err)
		}
		baseDir = filepath.Join(home, ".stoke", "stance-keys")
	}
	stanceDir := filepath.Join(baseDir, stance)
	keyPath := filepath.Join(stanceDir, "id_ed25519")
	pubPath := filepath.Join(stanceDir, "id_ed25519.pub")

	identityMu.Lock()
	defer identityMu.Unlock()

	if _, err := os.Stat(keyPath); err != nil {
		if err := ensureKeyPair(stanceDir, keyPath, pubPath); err != nil {
			return nil, err
		}
	}

	return &Identity{
		Stance:         stance,
		KeyPath:        keyPath,
		PublicKeyPath:  pubPath,
		AuthorName:     "stoke-" + stance,
		AuthorEmail:    stance + "@stances.stoke.local",
		CommitterName:  "stoke-" + stance,
		CommitterEmail: stance + "@stances.stoke.local",
	}, nil
}

var identityMu sync.Mutex

// sanitizeStance normalizes a stance name for use as a filesystem
// path. Permits lowercase alphanum + dash; other characters become
// dashes. Empty input stays empty.
func sanitizeStance(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// ensureKeyPair generates a fresh ed25519 keypair at (keyPath, pubPath).
// Private key PEM-encoded with 0600 perms; public key in OpenSSH
// authorized_keys format with 0644 perms. Errors on any filesystem
// issue — we never want a silent failure here because a missing key
// would force git commits to fail, not fall back to unsigned.
func ensureKeyPair(dir, keyPath, pubPath string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("stancesign: mkdir %s: %w", dir, err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("stancesign: generate key: %w", err)
	}
	// PEM-encode the private key in OpenSSH format so git's ssh signer
	// can consume it without a conversion step.
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return fmt.Errorf("stancesign: marshal private: %w", err)
	}
	var buf bytes.Buffer
	if err := pem.Encode(&buf, pemBlock); err != nil {
		return fmt.Errorf("stancesign: pem encode: %w", err)
	}
	if err := os.WriteFile(keyPath, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("stancesign: write private: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("stancesign: public key: %w", err)
	}
	authorizedLine := ssh.MarshalAuthorizedKey(sshPub)
	if err := os.WriteFile(pubPath, authorizedLine, 0o644); err != nil { // #nosec G306 -- SSH public key file; 0644 is standard.
		return fmt.Errorf("stancesign: write public: %w", err)
	}
	return nil
}
