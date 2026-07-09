// merkle.go — the canonical Merkle primitives the Anchorer's default backend
// commits under: a sha256 binary tree (duplicate-last-on-odd), leaf/internal
// domain separation, inclusion proofs, and empty-interval proofs. The exact
// hashing rule mirrors PORTS.md section 4 so it is byte-portable across ports.
package honestcrypto

import (
	"crypto/sha256"
	"encoding/hex"
)

func h(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// hashPair hashes a pair of node hashes in fixed left||right order.
func hashPair(left, right string) string {
	return h(left + right)
}

// LeafHash hashes a value into a leaf, domain-separated from internal nodes
// (prefix 0x00) to prevent second-preimage tricks.
func LeafHash(value string) string {
	return h("\x00" + value)
}

// InclusionProof binds a leaf's Merkle proof to a specific tree size.
type InclusionProof struct {
	// Index is the leaf index this proof is for.
	Index int `json:"index"`
	// Siblings are the sibling hashes bottom-up.
	Siblings []string `json:"siblings"`
	// Size is the total leaf count the proof was built against.
	Size int `json:"size"`
}

// MerkleRoot builds a Merkle root over already-leaf-hashed values. An empty tree
// hashes to sha256("").
func MerkleRoot(leaves []string) string {
	if len(leaves) == 0 {
		return h("")
	}
	level := make([]string, len(leaves))
	copy(level, leaves)
	for len(level) > 1 {
		next := make([]string, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			left := level[i]
			right := left // duplicate last on odd
			if i+1 < len(level) {
				right = level[i+1]
			}
			next = append(next, hashPair(left, right))
		}
		level = next
	}
	return level[0]
}

// InclusionProofFor produces an inclusion proof for a leaf index against the
// leaf set. It returns ok=false if index is out of range.
func InclusionProofFor(leaves []string, index int) (InclusionProof, bool) {
	if index < 0 || index >= len(leaves) {
		return InclusionProof{}, false
	}
	siblings := []string{}
	idx := index
	level := make([]string, len(leaves))
	copy(level, leaves)
	for len(level) > 1 {
		isRight := idx%2 == 1
		siblingIdx := idx + 1
		if isRight {
			siblingIdx = idx - 1
		}
		sibling := level[idx]
		if siblingIdx < len(level) {
			sibling = level[siblingIdx]
		}
		siblings = append(siblings, sibling)
		next := make([]string, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			left := level[i]
			right := left
			if i+1 < len(level) {
				right = level[i+1]
			}
			next = append(next, hashPair(left, right))
		}
		level = next
		idx /= 2
	}
	return InclusionProof{Index: index, Siblings: siblings, Size: len(leaves)}, true
}

// VerifyInclusion recomputes the root from the leaf + siblings and compares to
// the expected root.
func VerifyInclusion(leaf string, proof InclusionProof, expectedRoot string) bool {
	hash := leaf
	idx := proof.Index
	for _, sibling := range proof.Siblings {
		if idx%2 == 1 {
			hash = hashPair(sibling, hash)
		} else {
			hash = hashPair(hash, sibling)
		}
		idx /= 2
	}
	return hash == expectedRoot
}

// IntervalBracket is one side of an empty-interval proof: a leaf plus its
// inclusion proof.
type IntervalBracket struct {
	Leaf  string         `json:"leaf"`
	Proof InclusionProof `json:"proof"`
}

// EmptyIntervalProof attests that between two adjacent committed leaves there is
// nothing — used to prove "no record exists for subject X in window [t0,t1)". A
// verifier checks both brackets are included and adjacent by index.
type EmptyIntervalProof struct {
	Root string `json:"root"`
	// Before is the leaf immediately before the interval (nil if the interval
	// starts at genesis).
	Before *IntervalBracket `json:"before"`
	// After is the leaf immediately after the interval (nil if the interval runs
	// to the tip).
	After *IntervalBracket `json:"after"`
}

// VerifyEmptyInterval verifies an empty-interval proof against its root: both
// brackets included and consecutive by leaf index.
func VerifyEmptyInterval(p EmptyIntervalProof) bool {
	if p.Before != nil && !VerifyInclusion(p.Before.Leaf, p.Before.Proof, p.Root) {
		return false
	}
	if p.After != nil && !VerifyInclusion(p.After.Leaf, p.After.Proof, p.Root) {
		return false
	}
	if p.Before != nil && p.After != nil {
		return p.After.Proof.Index == p.Before.Proof.Index+1
	}
	return true
}
