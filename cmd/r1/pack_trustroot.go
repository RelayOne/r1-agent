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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/skill"
	"github.com/RelayOne/r1/internal/skillmfr"
)

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
