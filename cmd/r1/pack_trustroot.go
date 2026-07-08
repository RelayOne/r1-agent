package main

// pack_trustroot.go — C7 T6b helper: trust-root enforcement attached
// to the v1 load path.
//
// Behavior is GATED on trust-root presence: when no document exists
// at any candidate repo path, this helper returns nil and v1
// behavior is preserved. When a document exists AND the pack
// carries a signature, the kid is matched against the trust root.
// Unsigned packs always pass (the v1 path already does not require
// a signature).

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/skill"
	"github.com/RelayOne/r1/internal/skillmfr"
)

// trustRootPin reads the operator-pinned root public key from
// R1_TRUST_ROOT_PUBKEY (base64-encoded ed25519 public key). When set, the
// trust-root document's OWN signature is verified against it (fail-closed).
// Without this, MatchKey trusts every key LISTED in the document, so an
// attacker who appends their key to trust-root.json is honored — the exact
// tamper the document signature exists to prevent. When the pin is unset,
// v1 fallback is preserved (document-signature verification is skipped).
func trustRootPin() (ed25519.PublicKey, bool, error) {
	raw := strings.TrimSpace(os.Getenv("R1_TRUST_ROOT_PUBKEY"))
	if raw == "" {
		return nil, false, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, true, fmt.Errorf("R1_TRUST_ROOT_PUBKEY: base64 decode: %w", err)
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, true, fmt.Errorf("R1_TRUST_ROOT_PUBKEY: want %d-byte ed25519 key, got %d", ed25519.PublicKeySize, len(key))
	}
	return ed25519.PublicKey(key), true, nil
}

// verifyTrustRootDoc verifies the trust-root document's own signature
// against the operator-pinned root key when one is configured. No pin → nil
// (v1 fallback). A configured pin makes an unsigned or tampered document
// fail-closed via skill.VerifyTrustRoot.
func verifyTrustRootDoc(doc *skill.TrustRootDocument) error {
	pin, present, err := trustRootPin()
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	return skill.VerifyTrustRoot(doc, pin)
}

// enforceTrustRootForLoad walks upward from packPath looking for a
// trust-root document, then matches the pack's signature kid against
// it. Missing document / empty key set returns nil (v1 fallback).
func enforceTrustRootForLoad(packPath, packName string, signature *skillmfr.PackSignature) error {
	if signature == nil {
		return nil
	}
	doc, err := findEnclosingTrustRoot(packPath)
	if err != nil {
		return err
	}
	if doc == nil || len(doc.Keys) == 0 {
		return nil
	}
	// Verify the document's own signature against the pinned root key
	// (when configured) BEFORE trusting any key it lists.
	if err := verifyTrustRootDoc(doc); err != nil {
		return fmt.Errorf("trust root document: %w", err)
	}
	if _, err := skill.MatchKey(doc, signature.KeyID, packName, time.Now().UTC()); err != nil {
		if errors.Is(err, skill.ErrTrustRootKeyNotFound) {
			return fmt.Errorf("key_id %s not in trust root", signature.KeyID)
		}
		return err
	}
	return nil
}

// findEnclosingTrustRoot walks up from packPath looking for a
// repository-rooted trust-root document. Stops at filesystem root.
// Returns (nil, nil) when no document is found — caller treats that
// as v1 fallback.
func findEnclosingTrustRoot(packPath string) (*skill.TrustRootDocument, error) {
	dir, err := filepath.Abs(packPath)
	if err != nil {
		return nil, err
	}
	for {
		for _, root := range []string{r1dir.Canonical, r1dir.Legacy} {
			candidate := filepath.Join(dir, root, "skills", "trust-root.json")
			if _, statErr := os.Stat(candidate); statErr == nil {
				return skill.LoadTrustRoot(candidate)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		dir = parent
	}
}
