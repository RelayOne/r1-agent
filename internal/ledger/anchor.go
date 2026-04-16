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
	// computed with. Zero is treated as version 1 for
	// backward-compat with pre-versioned anchors. New anchors
	// always use AnchorSchemaVersion.
	SchemaVersion int `json:"schema_version,omitempty"`

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
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, fname), body, 0o644); err != nil {
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
//	v1 (legacy): PrevHash || MerkleRoot || IntervalEnd
//	v2 (current): PrevHash || MerkleRoot || IntervalStart || IntervalEnd
//
// Version 0 is treated as v1 for backward compatibility with
// anchors written by the original implementation that predated
// the SchemaVersion field.
func composeAnchorInput(version int, prevHash, merkleRoot string, intervalStart, intervalEnd time.Time) string {
	end := intervalEnd.UTC().Format(time.RFC3339Nano)
	start := intervalStart.UTC().Format(time.RFC3339Nano)
	switch version {
	case 0, 1:
		return prevHash + merkleRoot + end
	default:
		return prevHash + merkleRoot + start + end
	}
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

	leaves := make([][]byte, len(sorted))
	for i, hx := range sorted {
		b, err := hex.DecodeString(hx)
		if err != nil {
			// Caller passed a non-hex leaf; fall back to hashing the
			// raw string so the anchor still commits to SOMETHING
			// rather than silently skipping.
			sum := sha256.Sum256([]byte(hx))
			leaves[i] = sum[:]
			continue
		}
		leaves[i] = b
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
		// Verify with the anchor's own schema version so anchors
		// persisted under v1 continue to pass after the v2 bump.
		if a.NodeCount == 0 {
			emptyInput := fmt.Sprintf(EmptyIntervalTemplate, a.IntervalEnd.UTC().Format(time.RFC3339Nano))
			emptySum := sha256.Sum256([]byte(emptyInput))
			expectedRoot := hex.EncodeToString(emptySum[:])
			if a.MerkleRoot != expectedRoot {
				violations = append(violations, fmt.Sprintf("anchor %d NodeCount=0 but MerkleRoot does not match empty-interval commitment for intervalEnd %s", i, a.IntervalEnd.Format(time.RFC3339Nano)))
			}
		}
		anchorInput := composeAnchorInput(a.SchemaVersion, a.PrevHash, a.MerkleRoot, a.IntervalStart, a.IntervalEnd)
		sum := sha256.Sum256([]byte(anchorInput))
		if hex.EncodeToString(sum[:]) != a.Hash {
			shape := "PrevHash || MerkleRoot || IntervalStart || IntervalEnd (v2)"
			if a.SchemaVersion == 0 || a.SchemaVersion == 1 {
				shape = "PrevHash || MerkleRoot || IntervalEnd (v1)"
			}
			violations = append(violations, fmt.Sprintf("anchor %d Hash composition invalid — expected %s", i, shape))
		}
	}
	return violations
}
