// Package analyze implements the static analyzer for R1Skill DSL.
//
// The analyzer is an 8-stage pipeline. Each stage either passes or
// produces typed diagnostics. Stages do NOT fail-fast; the analyzer
// collects all diagnostics across all stages and returns them together,
// matching Go-compiler ergonomics: an LLM author seeing 12 errors fixes
// 12 things in one revision rather than 12 sequential round-trips.
//
// The successful output is a CompileProof: a structured artifact
// recording every check that passed, the IR hash, the analyzer version,
// and the constitution hash the skill was checked against. The proof is
// a ledger node; the runtime refuses to execute a skill without a valid
// proof whose IR hash matches the skill being loaded.
//
// This package only declares the entry point and the proof type. The
// actual stage implementations live in stage_*.go files in this same
// package; this file is the public API and the orchestration.
package analyze

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/RelayOne/r1/internal/r1skill/ir"
)

// AnalyzerVersion is the analyzer's own version. Bumped when stage logic
// changes in ways that could re-classify a previously-accepted or
// previously-rejected skill. Recorded on every CompileProof so a stale
// proof against an old analyzer is detectable.
const AnalyzerVersion = "1.0.0"

// CompileProof records that a skill IR passed all 8 analyzer stages
// against a specific constitution version. Persisted as a ledger node
// of type "skill.compile_proof".
type CompileProof struct {
	SkillID                   string        `json:"skill_id"`
	SkillVersion              int           `json:"skill_version"`
	IRHash                    string        `json:"ir_hash"`
	AnalyzerVersion           string        `json:"analyzer_version"`
	ConstitutionHash          string        `json:"constitution_hash"`
	Checks                    []StageResult `json:"checks"`
	RuntimeAssertionsInserted []string      `json:"runtime_assertions_inserted,omitempty"`
	VerifiedAt                time.Time     `json:"verified_at"`
	VerifiedBy                string        `json:"verified_by"`
}

// StageResult records one stage of the pipeline's outcome.
type StageResult struct {
	Stage       string       `json:"stage"`
	Passed      bool         `json:"passed"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Diagnostic is one finding from a stage. Levels: "error" halts
// compilation, "warning" surfaces but doesn't halt, "info" is purely
// observational.
type Diagnostic struct {
	Stage    string `json:"stage"`
	Level    string `json:"level"` // "error" | "warning" | "info"
	Code     string `json:"code"`  // structured error code (e.g. "E001_TYPE_MISMATCH")
	Message  string `json:"message"`
	Location string `json:"location,omitempty"` // node name or expression path
	Hint     string `json:"hint,omitempty"`     // suggested fix for LLM authoring
}

// Constitution is the subset of r1.constitution.yaml relevant to skill
// analysis. Loaded once per analyzer run and hashed.
//
// In production this struct is populated from the existing
// internal/policy package; keeping it here as a value type means the
// analyzer can be unit-tested without dragging in the full constitution
// loader.
type Constitution struct {
	ForbidShellPatterns              []string
	ForbidFSWritePaths               []string
	ForbidNetworkDomains             []string
	RequireLineageForLLMAuthored     bool
	DefaultCapsForLLMAuthored        ir.Capabilities
	ApprovalRequiredForWideningRules []string

	// Hash of the constitution at the time of loading. The proof embeds
	// this; if the constitution changes, all proofs are invalidated and
	// skills must be re-analyzed.
	Hash string
}

// Options controls analyzer behavior.
type Options struct {
	// CollectAllErrors is true by default; the analyzer accumulates
	// diagnostics across stages instead of halting on the first error.
	// Set to false for performance-critical paths where the first error
	// is sufficient signal.
	CollectAllErrors bool

	// AllowWarnings is true by default; warnings don't fail the build.
	// Set to false for "treat warnings as errors" mode.
	AllowWarnings bool
}

// DefaultOptions returns the recommended options for normal use.
func DefaultOptions() Options {
	return Options{CollectAllErrors: true, AllowWarnings: true}
}

// Analyze runs the 8-stage pipeline on a skill IR. Returns a CompileProof
// on success (all stages pass per the Options) or a non-nil error along
// with the partial proof so callers can surface diagnostics even on
// failure.
func Analyze(skill *ir.Skill, constitution Constitution, opts Options) (*CompileProof, error) {
	// Structural sanity check first
	if err := skill.Validate(); err != nil {
		return nil, &AnalyzerError{
			Diagnostics: []Diagnostic{{
				Stage:   "pre-flight",
				Level:   "error",
				Code:    "E000_MALFORMED_IR",
				Message: err.Error(),
			}},
		}
	}

	// Compute the IR hash deterministically. The hash is over the
	// canonical JSON encoding of the skill; small textual differences
	// (whitespace) round-trip to the same struct so this is stable.
	irHash, err := hashIR(skill)
	if err != nil {
		return nil, &AnalyzerError{
			Diagnostics: []Diagnostic{{
				Stage:   "pre-flight",
				Level:   "error",
				Code:    "E001_HASH_FAILED",
				Message: err.Error(),
			}},
		}
	}

	proof := &CompileProof{
		SkillID:          skill.SkillID,
		SkillVersion:     skill.SkillVersion,
		IRHash:           irHash,
		AnalyzerVersion:  AnalyzerVersion,
		ConstitutionHash: constitution.Hash,
		VerifiedAt:       time.Now().UTC(),
		VerifiedBy:       "r1skill-analyzer-" + AnalyzerVersion,
	}

	stages := []stageFn{
		{name: "schema", fn: stageSchema},
		{name: "type", fn: stageType},
		{name: "capability", fn: stageCapability},
		{name: "constitution", fn: stageConstitution},
		{name: "contract", fn: stageContract},
		{name: "termination", fn: stageTermination},
		{name: "replay", fn: stageReplay},
	}

	var allDiags []Diagnostic
	failed := false
	for _, st := range stages {
		result := st.fn(skill, &constitution)
		result.Stage = st.name
		proof.Checks = append(proof.Checks, result)
		if !result.Passed {
			failed = true
			allDiags = append(allDiags, result.Diagnostics...)
			if !opts.CollectAllErrors {
				break
			}
		} else {
			// Still collect non-error diagnostics for the proof
			for _, d := range result.Diagnostics {
				if d.Level == "warning" || d.Level == "info" {
					allDiags = append(allDiags, d)
				}
			}
		}
	}

	if failed {
		return proof, &AnalyzerError{Diagnostics: allDiags}
	}

	// Final stage: emit the proof artifact (stage 8 in the pipeline; trivial
	// if we got here)
	proof.Checks = append(proof.Checks, StageResult{
		Stage:  "proof",
		Passed: true,
	})

	return proof, nil
}

// hashIR produces a stable sha256 over the skill IR for the proof. We
// strip the Lineage timestamp before hashing because the same skill
// authored at two different moments should hash identically — the
// content is what matters for the proof, not the timestamp.
//
// In production we may want a more sophisticated canonical-JSON pass to
// ensure identical hashes across implementations; the stdlib encoding is
// stable enough for now and easier to audit.
func hashIR(skill *ir.Skill) (string, error) {
	// Clone-and-zero the timestamp so it doesn't influence the hash
	clone := *skill
	clone.Lineage.AuthoredAt = time.Time{}

	data, err := json.Marshal(&clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// stageFn pairs a stage name with its implementation.
type stageFn struct {
	name string
	fn   func(*ir.Skill, *Constitution) StageResult
}

// AnalyzerError is the structured error type. Carries diagnostics so
// callers can render them in their preferred format (CLI, JSON, ledger).
type AnalyzerError struct {
	Diagnostics []Diagnostic
}

func (e *AnalyzerError) Error() string {
	if len(e.Diagnostics) == 0 {
		return "r1skill/analyze: unspecified error"
	}
	if len(e.Diagnostics) == 1 {
		return "r1skill/analyze: " + e.Diagnostics[0].Code + ": " + e.Diagnostics[0].Message
	}
	return "r1skill/analyze: " + e.Diagnostics[0].Code + ": " + e.Diagnostics[0].Message + " (and more)"
}
