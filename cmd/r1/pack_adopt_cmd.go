package main

// pack_adopt_cmd.go — C7 cross-product-skill-exchange T5/T6b/T10/T14.
//
// `r1 skills pack adopt --pack <id> --for <product>` resolves a pack
// via the existing source-resolution helpers, loads its v2 manifest,
// verifies the target product is in compat[], dispatches to the
// matching adapter under internal/skill/compat/, writes the wrapper
// under <repoRoot>/.r1/skills/packs/<id>/wrappers/<product>.wrapper,
// and emits a signed `pack.adopted` event to the ledger.
//
// Trust-root enforcement (T6b): when a trust-root document exists at
// <repoRoot>/.r1/skills/trust-root.json AND the pack carries a
// signature, the kid in the signature is matched against the trust
// root before adopt proceeds. Missing trust root falls back to v1
// signature-only check (matches the spec's Business Logic § Trust
// verification step 4 / fallback rule).

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/skill"
	"github.com/RelayOne/r1/internal/skill/compat"
	"github.com/RelayOne/r1/internal/skillmfr"
)

// skillPackAdoptResult is the structured output of the adopt run.
// Returned by adoptSkillPack so the command + tests share the shape.
type skillPackAdoptResult struct {
	PackName        string
	PackVersion     string
	SourcePath      string
	TargetProduct   string
	WrapperPath     string
	WrapperBytes    int
	LedgerNodeID    string
	SignerKeyID     string
	TenantID        string
	AdoptedAt       string
}

// adoptValidTargets is the closed-set allowlist for --for. Anything
// else returns "unsupported adoption target".
var adoptValidTargets = map[string]struct{}{
	"r1":         {},
	"cloudswarm": {},
	"heroa":      {},
	"veritize":   {},
}

func runSkillsPackAdoptCmd(args []string) {
	repoRoot, packName, target := parseSkillPackAdoptArgs(args)
	result, err := adoptSkillPack(repoRoot, packName, target, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills pack adopt: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout,
		"pack: %s\nversion: %s\nsource: %s\ntarget: %s\nwrapper: %s\nwrapper_bytes: %d\nledger_node: %s\nsigner_key_id: %s\ntenant_id: %s\nadopted_at: %s\n",
		result.PackName,
		result.PackVersion,
		result.SourcePath,
		result.TargetProduct,
		result.WrapperPath,
		result.WrapperBytes,
		result.LedgerNodeID,
		result.SignerKeyID,
		result.TenantID,
		result.AdoptedAt,
	)
	// Operator next steps — printed to stderr so the structured stdout
	// stays machine-parseable.
	fmt.Fprintf(os.Stderr, "next steps:\n")
	fmt.Fprintf(os.Stderr, "  1. copy %s into the %s product's skill registry\n", result.WrapperPath, target)
	fmt.Fprintf(os.Stderr, "  2. configure %s_R1_SKILL_PACK_PATH=%s in the target runtime\n",
		strings.ToUpper(target), result.SourcePath)
	fmt.Fprintf(os.Stderr, "  3. verify the adoption with `r1 skills pack verify --pack %s`\n", result.PackName)
}

func parseSkillPackAdoptArgs(args []string) (string, string, string) {
	fs := flag.NewFlagSet("skills pack adopt", flag.ExitOnError)
	repoRoot := fs.String("repo", ".", "repository root")
	packName := fs.String("pack", "", "pack name under repo or user .r1|.stoke/skills/packs/")
	target := fs.String("for", "", "target product (one of r1|cloudswarm|heroa|veritize)")
	fs.Parse(args)
	if *packName == "" {
		fmt.Fprintln(os.Stderr, "skills pack adopt: --pack is required")
		os.Exit(2)
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "skills pack adopt: --for is required (one of r1|cloudswarm|heroa|veritize)")
		os.Exit(2)
	}
	if _, ok := adoptValidTargets[*target]; !ok {
		fmt.Fprintf(os.Stderr, "skills pack adopt: unsupported adoption target: %s (one of r1|cloudswarm|heroa|veritize)\n", *target)
		os.Exit(2)
	}
	return *repoRoot, *packName, *target
}

// adoptSkillPack is the testable core. now is parameterized so unit
// tests can stamp deterministic timestamps onto the emitted event.
func adoptSkillPack(repoRoot, packName, target string, now time.Time) (*skillPackAdoptResult, error) {
	if _, ok := adoptValidTargets[target]; !ok {
		return nil, fmt.Errorf("unsupported adoption target: %s", target)
	}
	repoAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root: %w", err)
	}
	sourcePath, err := resolveSkillPackSource(repoAbs, packName)
	if err != nil {
		return nil, err
	}
	pack, signature, err := loadSkillPackWithSignature(sourcePath)
	if err != nil {
		return nil, err
	}
	manifest, err := skill.LoadManifestV2(sourcePath)
	if err != nil {
		return nil, err
	}
	if err := manifest.CheckCompat(target); err != nil {
		return nil, fmt.Errorf("pack %s not compatible with %s (compat=%v)",
			pack.Meta.Name, target, manifest.Compat)
	}
	// Trust-root match (T6b). Optional: missing doc -> v1 fallback.
	tenantID, err := checkTrustRootForAdopt(repoAbs, pack.Meta.Name, signature, now)
	if err != nil {
		return nil, err
	}
	wrapperBytes, err := compat.Adapt(target, manifest)
	if err != nil {
		return nil, err
	}
	wrapperDir := filepath.Join(sourcePath, "wrappers")
	if err := os.MkdirAll(wrapperDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir wrappers: %w", err)
	}
	wrapperPath := filepath.Join(wrapperDir, target+".wrapper")
	if err := os.WriteFile(wrapperPath, wrapperBytes, 0o644); err != nil {
		return nil, fmt.Errorf("write wrapper: %w", err)
	}

	// Emit the pack.adopted ledger event. The ledger root lives under
	// <repoRoot>/<r1dir>/ledger. Single-shot Open is fine — adopt is
	// invoked from the CLI, not an inner loop.
	ledgerRoot := filepath.Join(repoAbs, r1dir.CanonicalPath("ledger"))
	if err := os.MkdirAll(ledgerRoot, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir ledger: %w", err)
	}
	store, err := ledger.NewStore(ledgerRoot)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	signerKID := ""
	if signature != nil {
		signerKID = signature.KeyID
	}
	payload := ledger.PackAdoptedPayload{
		PackID:        pack.Meta.Name,
		PackVersion:   pack.Meta.Version,
		TargetProduct: target,
		TenantID:      tenantID,
		SignerKeyID:   signerKID,
		AdoptedAt:     now.Format(time.RFC3339),
	}
	// Sign the payload if a signing key exists; otherwise persist
	// unsigned (legacy mode).
	if priv, err := loadAdoptSigningKey(repoAbs); err == nil && priv != nil {
		signed, serr := ledger.SignPackAdopted(payload, priv)
		if serr != nil {
			return nil, fmt.Errorf("sign pack_adopted: %w", serr)
		}
		payload = signed
	}
	nodeID := ledger.NodeID(deriveAdoptNodeID(pack.Meta.Name, target, payload.AdoptedAt))
	if _, err := ledger.PersistPackAdopted(store, nodeID, payload, "skills_pack_adopt_cmd"); err != nil {
		return nil, err
	}
	return &skillPackAdoptResult{
		PackName:      pack.Meta.Name,
		PackVersion:   pack.Meta.Version,
		SourcePath:    sourcePath,
		TargetProduct: target,
		WrapperPath:   wrapperPath,
		WrapperBytes:  len(wrapperBytes),
		LedgerNodeID:  string(nodeID),
		SignerKeyID:   signerKID,
		TenantID:      tenantID,
		AdoptedAt:     payload.AdoptedAt,
	}, nil
}

// checkTrustRootForAdopt enforces trust-root matching when a
// trust-root document is present. Returns the matched entry's
// tenant_id (empty for non-tenant authorities).
//
// Missing trust-root document AND missing signature both fall back
// to v1 behavior — no enforcement, empty tenantID — per spec.
func checkTrustRootForAdopt(repoAbs, packName string, signature *skillmfr.PackSignature, now time.Time) (string, error) {
	if signature == nil {
		// Unsigned pack: nothing to enforce; v1 fallback already
		// chose not to refuse load.
		return "", nil
	}
	trustRootPath := skill.TrustRootPathFor(repoAbs, r1dir.Canonical)
	doc, err := skill.LoadTrustRoot(trustRootPath)
	if err != nil {
		return "", fmt.Errorf("load trust root: %w", err)
	}
	if doc == nil || len(doc.Keys) == 0 {
		// Trust root absent — fall back to v1 signature-only.
		return "", nil
	}
	// Verify the document's own signature against the pinned root key
	// (when configured) BEFORE trusting any key it lists.
	if err := verifyTrustRootDoc(doc); err != nil {
		return "", fmt.Errorf("trust root document: %w", err)
	}
	entry, err := skill.MatchKey(doc, signature.KeyID, packName, now)
	if err != nil {
		if errors.Is(err, skill.ErrTrustRootKeyNotFound) {
			return "", fmt.Errorf("key_id %s not in trust root", signature.KeyID)
		}
		return "", err
	}
	return entry.TenantID, nil
}

// loadAdoptSigningKey returns the operator's pack-adopted signing
// key, generating one on first call. Persisted under
// <repo>/.r1/skills/adopt-signing.key. Empty path / missing key
// returns nil priv (legacy unsigned mode).
func loadAdoptSigningKey(repoAbs string) (ed25519.PrivateKey, error) {
	keyPath := filepath.Join(repoAbs, r1dir.CanonicalPath("skills"), "adopt-signing.key")
	priv, _, err := skill.LoadOrGenerateRootKey(keyPath)
	return priv, err
}

// deriveAdoptNodeID returns a stable per-adoption ledger node id.
// Format: "pack-adopted-<short-sha256(packName + target + adoptedAt)>".
// The hash is content-addressed so re-emitting the SAME adoption
// (same pack, same target, same timestamp) is idempotent.
func deriveAdoptNodeID(packName, target, adoptedAt string) string {
	sum := sha256.Sum256([]byte(packName + "|" + target + "|" + adoptedAt))
	return "pack-adopted-" + hex.EncodeToString(sum[:8])
}
