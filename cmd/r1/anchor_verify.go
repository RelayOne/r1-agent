package main

// anchor_verify.go — canonical Anchorer verification shared by
// `r1 verify --anchor` and `r1 receipt verify --anchor`.
//
// Both entry points verify (a) a canonical honest-crypto record's own envelope
// hash and (b) the record's inclusion under an AnchorProof, routed through the
// active Anchorer seam (internal/honestcrypto.GetAnchorer). The call site names
// no concrete backend, so a stronger anchor backend swaps in by config with zero
// changes here (the default backend is a trusted timestamp over a Merkle
// commitment; see the swap-proof test).

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/RelayOne/r1/internal/honestcrypto"
)

// anchorVerifyResult is the outcome of a canonical anchor verification.
type anchorVerifyResult struct {
	RecordHash  string `json:"record_hash"`
	Backend     string `json:"backend"`
	EnvelopeOK  bool   `json:"envelope_ok"`  // record's own hash recomputes (envelope integrity)
	InclusionOK bool   `json:"inclusion_ok"` // record hash included under the proof via the active Anchorer
}

// OK reports whether both checks passed.
func (r anchorVerifyResult) OK() bool { return r.EnvelopeOK && r.InclusionOK }

func loadJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// verifyCanonicalAnchor verifies a canonical record against an anchor proof
// through the active Anchorer seam. It is backend-agnostic: whatever Anchorer is
// registered handles inclusion verification.
func verifyCanonicalAnchor(recordPath, proofPath string) (anchorVerifyResult, error) {
	var rec honestcrypto.Record
	if err := loadJSONFile(recordPath, &rec); err != nil {
		return anchorVerifyResult{}, fmt.Errorf("load record %s: %w", recordPath, err)
	}
	var proof honestcrypto.AnchorProof
	if err := loadJSONFile(proofPath, &proof); err != nil {
		return anchorVerifyResult{}, fmt.Errorf("load proof %s: %w", proofPath, err)
	}
	return anchorVerifyResult{
		RecordHash:  rec.Hash,
		Backend:     proof.Backend,
		EnvelopeOK:  honestcrypto.VerifyRecordHash(rec),
		InclusionOK: honestcrypto.GetAnchorer().Verify(rec.Hash, proof),
	}, nil
}

// runAnchorVerify is the CLI body shared by both commands. Exit codes: 0 both
// checks pass, 1 a check failed, 2 an IO/parse error.
func runAnchorVerify(recordPath, proofPath string, stdout, stderr io.Writer) int {
	if recordPath == "" || proofPath == "" {
		fmt.Fprintln(stderr, "anchor verify: --record and --proof are both required")
		return 2
	}
	res, err := verifyCanonicalAnchor(recordPath, proofPath)
	if err != nil {
		fmt.Fprintf(stderr, "anchor verify: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "record %s\n", res.RecordHash)
	fmt.Fprintf(stdout, "  envelope: %s\n", passFail(res.EnvelopeOK))
	fmt.Fprintf(stdout, "  anchor[%s]: %s\n", res.Backend, passFail(res.InclusionOK))
	if res.OK() {
		return 0
	}
	return 1
}

func passFail(ok bool) string {
	if ok {
		return "OK"
	}
	return "FAIL"
}
