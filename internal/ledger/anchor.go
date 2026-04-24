// Package ledger — anchor.go
//
// Merkle commitments + empty-interval proofs for the reasoning
// ledger (S-U-009). Adds a tamper-evident chain on top of the
// existing content-addressed graph so operators can cryptographically
// verify "no decisions were made between t1 and t2" — not just
// "we have no records," which a malicious actor could forge by
// deletion.
//
// Established prior art: Google Trillian (Certificate Transparency),
// Sigstore Rekor (software supply chain), Google Key Transparency
// all use empty-interval Merkle commitments. TrustPlane's
// `audit-pipeline/src/anchoring.rs` is the reference implementation
// Stoke's pattern matches — same empty-interval commitment input
// shape, same anchor-hash composition, same 5-minute default cadence.
//
// This implementation is STRICTLY ADDITIVE: it reads from the
// existing node store, produces a separate anchor log, and never
// mutates or reorders existing nodes. Callers that don't invoke
// anchor.Run are unaffected.
package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// AnchorIntervalSeconds is the default commitment cadence. Matches
// TrustPlane's audit-pipeline 300-second window so a portfolio that
// runs both can correlate anchors across systems. Override via
// AnchorConfig.IntervalSeconds for tighter or looser cadence.
const AnchorIntervalSeconds = 300

// GenesisPrevHash is the canonical input used as the "previous
// anchor hash" for the first anchor in a chain. A well-known
// sentinel so verifiers can spot a chain that started mid-history
// without one (indicating ledger import or truncation).
const GenesisPrevHash = "STOKE_ANCHOR_GENESIS"

// EmptyIntervalTemplate is the canonical input format for an
// empty-interval commitment. Hashing "STOKE_EMPTY_INTERVAL:<rfc3339>"
// produces a distinct, verifier-reproducible digest so "no
// transactions in this window" is cryptographically distinguishable
// from "transactions occurred and were suppressed." An adversary
// who deleted nodes cannot forge a matching anchor without also
// producing a matching empty-interval input for the missing
// window — and the input includes the window's end timestamp,
// which the verifier knows independently from the anchor chain.
const EmptyIntervalTemplate = "STOKE_EMPTY_INTERVAL:%s"

// AnchorSchemaVersion records which Hash composition was used to
// persist an Anchor. Incremented when the composition changes so
// verifiers can recompute old anchors with their original shape.
//
// Versions:
//
//	1: sha256(PrevHash || MerkleRoot || IntervalEnd)
//	2: sha256(PrevHash || MerkleRoot || IntervalStart || IntervalEnd)
//	   (IntervalStart added so log proves its claimed lower bound)
const AnchorSchemaVersion = 2

// Anchor is one row in the anchor log. Hashes are hex-encoded
// SHA-256 digests. The chain is `PrevHash -> MerkleRoot -> Hash`
// via a schema-version-specific composition so any insertion,
// deletion, or reordering of nodes within an interval changes
// `MerkleRoot`, which changes every subsequent `Hash`.
type Anchor struct {
	// SchemaVersion records the Hash composition this anchor was
	// computed with. NEW anchors always set this to
	// AnchorSchemaVersion. Anchors with SchemaVersion==0 from
	// disk are TREATED AS LEGACY v1 for backward-compat with
	// the initial ff869d1 build, but only when the JSON row
	// genuinely lacks the field — stripped schema_version on an
	// originally-versioned row is detected via schemaVersionPresent
	// (populated by UnmarshalJSON) and surfaces as a violation.
	SchemaVersion int `json:"schema_version,omitempty"`

	// schemaVersionPresent is set by UnmarshalJSON when the raw
	// JSON contained a schema_version key (including when the
	// value is explicit null). Used by verifyAnchorHash to
	// distinguish "legacy v1 anchor (field absent)" from
	// "versioned anchor whose schema_version field was stripped
	// or nulled" — the latter must surface as a tamper violation
	// even though both deserialize as SchemaVersion==0.
	schemaVersionPresent bool `json:"-"`

	// schemaVersionNull distinguishes "schema_version":null
	// (explicit null, tampered) from a numeric value.
	schemaVersionNull bool `json:"-"`

	// Seq is the zero-indexed anchor number in this chain. Useful
	// for verifier diagnostics; NOT part of the hash composition
	// (which uses timestamp + prev-hash) so the chain stays valid
	// across anchor-log backfill.
	Seq int `json:"seq"`

	// IntervalStart is the inclusive lower bound of the window
	// this anchor covers (UTC). The commitment is over every node
	// with CreatedAt in [IntervalStart, IntervalEnd).
	IntervalStart time.Time `json:"interval_start"`

	// IntervalEnd is the exclusive upper bound. Typically
	// IntervalStart + AnchorIntervalSeconds, but variable-length
	// intervals are allowed (operator pause / resume, on-demand
	// anchoring).
	IntervalEnd time.Time `json:"interval_end"`

	// NodeCount is how many nodes the Merkle root was computed
	// over. Zero indicates an empty-interval anchor.
	NodeCount int `json:"node_count"`

	// MerkleRoot is hex-SHA-256. For non-empty intervals it's the
	// Merkle root of sha256(node.ID) leaves in ascending ID order.
	// For empty intervals it's sha256(EmptyIntervalTemplate format-
	// string with IntervalEnd.Format(time.RFC3339Nano)).
	MerkleRoot string `json:"merkle_root"`

	// PrevHash links this anchor to its predecessor. For the first
	// anchor, PrevHash = GenesisPrevHash.
	PrevHash string `json:"prev_hash"`

	// Hash is the anchor's own digest:
	//   sha256(PrevHash || MerkleRoot || IntervalEnd.Format(time.RFC3339Nano))
	// The next anchor's PrevHash must equal this.
	Hash string `json:"hash"`
}

// UnmarshalJSON captures whether the raw JSON actually contained a
// `schema_version` key so verifyAnchorHash can distinguish a legacy
// v1 anchor (field genuinely absent) from a tampered versioned
// anchor (field explicitly stripped to 0). Without this marker,
// both paths produce SchemaVersion==0 and the v0→v1 legacy-compat
// fallback silently accepts the tampered record.
func (a *Anchor) UnmarshalJSON(data []byte) error {
	// Use json.RawMessage for schema_version so we can distinguish
	// three cases json.Unmarshal collapses to the same *int nil:
	//
	//   1. key absent         → raw is zero-value RawMessage (len 0)
	//   2. "schema_version":null → raw == "null" (4 bytes)
	//   3. "schema_version":N    → raw == "N"
	//
	// Case 1 is the legacy ff869d1 shape. Cases 2 and 3 are
	// modern records — null is a tampered modern record (used to
	// be a number, now null) and must surface as a violation.
	type anchorJSON struct {
		SchemaVersion json.RawMessage `json:"schema_version,omitempty"`
		Seq           int             `json:"seq"`
		IntervalStart time.Time       `json:"interval_start"`
		IntervalEnd   time.Time       `json:"interval_end"`
		NodeCount     int             `json:"node_count"`
		MerkleRoot    string          `json:"merkle_root"`
		PrevHash      string          `json:"prev_hash"`
		Hash          string          `json:"hash"`
	}
	var raw anchorJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	a.Seq = raw.Seq
	a.IntervalStart = raw.IntervalStart
	a.IntervalEnd = raw.IntervalEnd
	a.NodeCount = raw.NodeCount
	a.MerkleRoot = raw.MerkleRoot
	a.PrevHash = raw.PrevHash
	a.Hash = raw.Hash
	a.schemaVersionNull = false
	trimmed := strings.TrimSpace(string(raw.SchemaVersion))
	switch {
	case len(raw.SchemaVersion) == 0:
		// Field genuinely absent → legacy ff869d1 anchor.
		a.SchemaVersion = 0
		a.schemaVersionPresent = false
	case trimmed == "null":
		// Explicit null — tampered modern record.
		a.SchemaVersion = 0
		a.schemaVersionPresent = true
		a.schemaVersionNull = true
	default:
		if err := json.Unmarshal(raw.SchemaVersion, &a.SchemaVersion); err != nil {
			return fmt.Errorf("schema_version: %w", err)
		}
		a.schemaVersionPresent = true
	}
	return nil
}

// AnchorStore persists anchors to disk alongside the ledger. One
// file per anchor under <rootDir>/anchors/<seq>-<hash-prefix>.json
// plus an index file <rootDir>/anchors/index.json listing all
// anchors in order. Read-only verifier operations use the index
// file; Run() appends.
type AnchorStore struct {
	rootDir string
	mu      sync.Mutex
}

// NewAnchorStore opens an anchor store rooted at rootDir. Creates
// the anchors subdirectory if missing. The store is safe for
// concurrent readers and serializes writes under a mutex.
func NewAnchorStore(rootDir string) (*AnchorStore, error) {
	dir := filepath.Join(rootDir, "anchors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create anchors dir: %w", err)
	}
	return &AnchorStore{rootDir: rootDir}, nil
}

// Append writes a new anchor to the index AND a per-anchor JSON
// file. To stay crash-consistent, the index.jsonl append happens
// FIRST with fsync (it's the authoritative source of chain state
// — LastAnchor / ReadChain / VerifyChain all read index.jsonl),
// and the per-anchor pretty-printed file is written second as a
// debugging / inspection aid. A crash between the two leaves the
// chain complete in index.jsonl; at most the pretty-print file is
// missing, which doesn't affect verification.
//
// Callers should derive the anchor via ComputeAnchor to ensure
// chain integrity.
func (s *AnchorStore) Append(a Anchor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.rootDir, "anchors")
	indexPath := filepath.Join(dir, "index.jsonl")

	// 1. Append to index.jsonl with fsync so the chain survives
	//    a crash between the index write and the pretty-print file.
	f, err := os.OpenFile(indexPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(a)
	line = append(line, '\n')
	if _, err = f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}

	// 2. Write the pretty-printed per-anchor inspection file. A
	//    failure here does NOT corrupt the chain — index.jsonl is
	//    already committed. Log via return value so operators see
	//    the partial state.
	prefix := a.Hash
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	fname := fmt.Sprintf("%06d-%s.json", a.Seq, prefix)
	body, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		// index already committed; surface the inspection-file
		// failure as a non-fatal diagnostic.
		return fmt.Errorf("anchor seq %d persisted to index.jsonl but inspection file marshal failed: %w", a.Seq, err)
	}
	if err := os.WriteFile(filepath.Join(dir, fname), body, 0o600); err != nil {
		return fmt.Errorf("anchor seq %d persisted to index.jsonl but inspection file write failed: %w", a.Seq, err)
	}
	return nil
}

// LastAnchor returns the most recently appended anchor, or (Anchor{},
// false) when the store is empty. Used by Run() to determine
// PrevHash + IntervalStart for the next anchor.
func (s *AnchorStore) LastAnchor() (Anchor, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	indexPath := filepath.Join(s.rootDir, "anchors", "index.jsonl")
	body, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Anchor{}, false, nil
		}
		return Anchor{}, false, err
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return Anchor{}, false, nil
	}
	last := lines[len(lines)-1]
	var a Anchor
	if err := json.Unmarshal([]byte(last), &a); err != nil {
		return Anchor{}, false, err
	}
	return a, true, nil
}

// ReadChain returns every anchor in seq order. Used by verifier
// tooling. Callers who only need the last anchor should use
// LastAnchor() for better performance on long chains.
func (s *AnchorStore) ReadChain() ([]Anchor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	indexPath := filepath.Join(s.rootDir, "anchors", "index.jsonl")
	body, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	out := make([]Anchor, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var a Anchor
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			return out, fmt.Errorf("corrupt anchor index at seq %d: %w", len(out), err)
		}
		out = append(out, a)
	}
	return out, nil
}

// LeafDigestForNode returns the canonical SHA-256 over the full node
// JSON that callers must use to compute Merkle leaves. Hashing
// node.ID alone would NOT commit to node content (the ledger's
// computeID truncates to 8 hex chars and Store.ReadNode does not
// re-hash on read), so a tampered node body could keep the same
// ID and go undetected. This helper hashes the canonical JSON
// marshal of the full Node struct so any content change breaks
// the Merkle root — which breaks every subsequent anchor.
func LeafDigestForNode(n Node) (string, error) {
	blob, err := json.Marshal(n)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:]), nil
}

// ComputeAnchor derives an anchor for the given interval over the
// leaf digests. Each leaf MUST be hex-SHA-256 of a full node's
// canonical JSON (use LeafDigestForNode). The caller's
// responsibility to collect nodes with CreatedAt in
// [intervalStart, intervalEnd) and hash each one. prevHash comes
// from the prior anchor's Hash (or GenesisPrevHash for the first).
//
// For empty intervals, MerkleRoot is the canonical empty-interval
// commitment:
//
//	sha256(fmt.Sprintf(EmptyIntervalTemplate, intervalEnd.UTC().Format(time.RFC3339Nano)))
//
// so a verifier can reproduce the value knowing only intervalEnd.
//
// IntervalStart is included in the anchor Hash composition so the
// log proves the claimed lower bound — rewriting interval_start
// after the fact breaks the chain. Composition:
//
//	Hash = sha256(PrevHash || MerkleRoot || IntervalStart || IntervalEnd)
//
// where IntervalStart and IntervalEnd are RFC3339Nano UTC.
func ComputeAnchor(seq int, intervalStart, intervalEnd time.Time, leafDigests []string, prevHash string) Anchor {
	if prevHash == "" {
		prevHash = GenesisPrevHash
	}
	var merkleRoot string
	if len(leafDigests) == 0 {
		input := fmt.Sprintf(EmptyIntervalTemplate, intervalEnd.UTC().Format(time.RFC3339Nano))
		sum := sha256.Sum256([]byte(input))
		merkleRoot = hex.EncodeToString(sum[:])
	} else {
		merkleRoot = merkleRootOfLeaves(leafDigests)
	}
	anchorInput := composeAnchorInput(AnchorSchemaVersion, prevHash, merkleRoot, intervalStart, intervalEnd)
	sum := sha256.Sum256([]byte(anchorInput))
	return Anchor{
		SchemaVersion: AnchorSchemaVersion,
		Seq:           seq,
		IntervalStart: intervalStart.UTC(),
		IntervalEnd:   intervalEnd.UTC(),
		NodeCount:     len(leafDigests),
		MerkleRoot:    merkleRoot,
		PrevHash:      prevHash,
		Hash:          hex.EncodeToString(sum[:]),
	}
}

// composeAnchorInput returns the string the Hash is SHA-256'd over
// for the given schema version. Kept as a single function so
// ComputeAnchor and VerifyChain stay in lockstep.
//
//	v1: PrevHash || MerkleRoot || IntervalEnd
//	v2: PrevHash || MerkleRoot || IntervalStart || IntervalEnd
//
// Unknown versions return the empty string — callers should
// recognize that as "unsupported schema" and surface a violation
// rather than accepting a bogus hash.
func composeAnchorInput(version int, prevHash, merkleRoot string, intervalStart, intervalEnd time.Time) string {
	end := intervalEnd.UTC().Format(time.RFC3339Nano)
	start := intervalStart.UTC().Format(time.RFC3339Nano)
	switch version {
	case 1:
		return prevHash + merkleRoot + end
	case 2:
		return prevHash + merkleRoot + start + end
	default:
		return ""
	}
}

// verifyAnchorHash checks whether the anchor's stored Hash matches
// the composition for its declared SchemaVersion. Returns "" on
// match, or a reason string describing the mismatch.
//
// Design note on v0 handling: anchors with SchemaVersion==0 are
// treated as v1 for backward compatibility with the initial
// anchor-store commit (ff869d1) which predated the SchemaVersion
// field. This preserves verification of any anchor logs produced
// on that build. A v2 anchor that had its schema_version field
// stripped (to flip to v0) would be recomputed with v1 composition
// — that hash does NOT match the v2 input shape, so the
// mismatch surfaces as a violation. Tamper-evidence is preserved:
// only genuinely v1-shape v0 records pass, downgrades fail.
func verifyAnchorHash(a Anchor) string {
	// Three rejection cases for non-legacy anchors where
	// schema_version should be load-bearing:
	//   - explicit null: tampered (was numeric, now null)
	//   - explicit 0: tampered (field present with invalid value)
	//   - explicit unknown value: rejected by composeAnchorInput
	// A genuinely absent field (legacy ff869d1 shape) is the only
	// case that falls through to v1 recompute.
	if a.schemaVersionNull {
		return "anchor has schema_version=null (tampered: field was numeric, now null); this build writes v2 and accepts v1+"
	}
	version := a.SchemaVersion
	if version == 0 {
		if a.schemaVersionPresent {
			return "anchor has explicit schema_version=0 (field set but invalid); this build writes v2 and accepts v1+"
		}
		version = 1
	}
	input := composeAnchorInput(version, a.PrevHash, a.MerkleRoot, a.IntervalStart, a.IntervalEnd)
	if input == "" {
		return fmt.Sprintf("anchor uses unknown schema_version %d; this build supports v1 and v2", a.SchemaVersion)
	}
	sum := sha256.Sum256([]byte(input))
	if hex.EncodeToString(sum[:]) != a.Hash {
		shape := "prev||merkle||start||end (v2)"
		if version == 1 {
			shape = "prev||merkle||end (v1)"
		}
		hint := ""
		if !a.schemaVersionPresent {
			hint = " (anchor has no schema_version; verified as v1 for legacy compat — a stripped v2 anchor would fail here)"
		}
		return "anchor Hash does not match " + shape + " composition" + hint
	}
	return ""
}

// merkleRootOfLeaves computes a binary Merkle root over hex-SHA-256
// leaves in ascending order. Duplicates are dropped — a content
// hash uniquely identifies one node's body, so duplicates can only
// arise from caller bugs. Odd-count levels duplicate the last leaf
// (Bitcoin-style).
func merkleRootOfLeaves(hexLeaves []string) string {
	if len(hexLeaves) == 0 {
		return ""
	}
	sorted := make([]string, len(hexLeaves))
	copy(sorted, hexLeaves)
	sort.Strings(sorted)
	j := 0
	for i := 0; i < len(sorted); i++ {
		if i == 0 || sorted[i] != sorted[i-1] {
			sorted[j] = sorted[i]
			j++
		}
	}
	sorted = sorted[:j]

	leaves := make([][]byte, 0, len(sorted))
	for _, hx := range sorted {
		b, err := hex.DecodeString(hx)
		if err != nil {
			// Caller passed a non-hex leaf; fall back to hashing the
			// raw string so the anchor still commits to SOMETHING
			// rather than silently skipping.
			sum := sha256.Sum256([]byte(hx))
			leaves = append(leaves, sum[:])
			continue
		}
		leaves = append(leaves, b)
	}
	for len(leaves) > 1 {
		if len(leaves)%2 == 1 {
			leaves = append(leaves, leaves[len(leaves)-1])
		}
		next := make([][]byte, 0, len(leaves)/2)
		for i := 0; i < len(leaves); i += 2 {
			pair := append([]byte{}, leaves[i]...)
			pair = append(pair, leaves[i+1]...)
			sum := sha256.Sum256(pair)
			next = append(next, sum[:])
		}
		leaves = next
	}
	return hex.EncodeToString(leaves[0])
}

// VerifyChain walks the anchor log and returns a list of integrity
// violations (empty on a clean chain). Checks:
//   - First anchor's PrevHash == GenesisPrevHash
//   - Each anchor's Hash matches its recomputed value
//   - Each anchor's PrevHash matches the previous anchor's Hash
//   - Seq is monotonic
//   - Empty-interval anchors have NodeCount == 0 and use the
//     canonical EmptyIntervalTemplate value as MerkleRoot
//
// The ledger nodes themselves are NOT re-hashed here (that would
// require reading the full ledger). Callers that want full
// verification should additionally enumerate nodes per interval
// and re-run ComputeAnchor; this function verifies the anchor
// CHAIN for internal consistency.
func (s *AnchorStore) VerifyChain() []string {
	chain, err := s.ReadChain()
	if err != nil {
		return []string{fmt.Sprintf("read chain: %v", err)}
	}
	if len(chain) == 0 {
		return nil
	}
	var violations []string
	for i, a := range chain {
		if a.Seq != i {
			violations = append(violations, fmt.Sprintf("anchor %d has Seq=%d (expected %d)", i, a.Seq, i))
		}
		expectedPrev := GenesisPrevHash
		if i > 0 {
			expectedPrev = chain[i-1].Hash
		}
		if a.PrevHash != expectedPrev {
			violations = append(violations, fmt.Sprintf("anchor %d PrevHash=%q expected %q", i, a.PrevHash, expectedPrev))
		}
		if a.NodeCount == 0 {
			emptyInput := fmt.Sprintf(EmptyIntervalTemplate, a.IntervalEnd.UTC().Format(time.RFC3339Nano))
			emptySum := sha256.Sum256([]byte(emptyInput))
			expectedRoot := hex.EncodeToString(emptySum[:])
			if a.MerkleRoot != expectedRoot {
				violations = append(violations, fmt.Sprintf("anchor %d NodeCount=0 but MerkleRoot does not match empty-interval commitment for intervalEnd %s", i, a.IntervalEnd.Format(time.RFC3339Nano)))
			}
		}
		if reason := verifyAnchorHash(a); reason != "" {
			violations = append(violations, fmt.Sprintf("anchor %d: %s", i, reason))
		}
	}
	return violations
}
