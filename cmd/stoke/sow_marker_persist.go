package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// SessionPersistMarker is a per-session completion marker that lets
// repeated SOW runs skip already-done sessions instead of re-running
// them and risking that the agent breaks something it already built.
//
// Two marker styles:
//   - File-tracked: Files map is non-empty. Validates spec hash AND every
//     listed file's sha256. Strict drift detection for sessions whose
//     output files are well-defined.
//   - Spec-only: Files map is empty. Validates spec hash only. Used when
//     a session was accepted as preexisting (agent wrote substantial work
//     but couldn't drive every test green within the repair budget).
type SessionPersistMarker struct {
	SessionID   string            `json:"session_id"`
	Title       string            `json:"title"`
	SpecHash    string            `json:"spec_hash"`
	Files       map[string]string `json:"files"`
	CompletedAt string            `json:"completed_at"`
	Note        string            `json:"note,omitempty"`
	// Provenance records which model/prompt/context produced this
	// session's work. Populated at marker-write time; used for
	// forensic replay ("which model actually wrote this?") and for
	// correlating session outputs to the SOW run that produced them.
	// SLSA/in-toto don't yet define an agent-provenance predicate —
	// this is stoke's pragmatic fill-in until a standard emerges.
	Provenance *SessionProvenance `json:"provenance,omitempty"`
}

// SessionProvenance is the model+context fingerprint for a session.
// All hashes are sha256 hex prefixes (16 chars) for compactness.
type SessionProvenance struct {
	WorkerModel        string `json:"worker_model,omitempty"`
	ReasoningModel     string `json:"reasoning_model,omitempty"`
	BaseURL            string `json:"base_url,omitempty"`
	UniversalCtxHash   string `json:"universal_context_hash,omitempty"`
	SOWSpecHash        string `json:"sow_spec_hash,omitempty"`
	SOWID              string `json:"sow_id,omitempty"`
	StokeVersion       string `json:"stoke_version,omitempty"`
	GitBaseSHA         string `json:"git_base_sha,omitempty"`
	ParallelWorkers    int    `json:"parallel_workers,omitempty"`
	ReviewerSplitUsed  bool   `json:"reviewer_split_used,omitempty"`
}

func sessionMarkerDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".stoke", "sow-state-markers")
}

func sessionMarkerPath(repoRoot, sessionID string) string {
	return filepath.Join(sessionMarkerDir(repoRoot), sessionID+".json")
}

// hashUpstreamSession produces a stable spec hash from session ID,
// description, sorted criteria descriptions/commands, and outputs.
func hashUpstreamSession(s plan.Session) string {
	var b strings.Builder
	b.WriteString(s.ID)
	b.WriteString("\n")
	b.WriteString(s.Title)
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(s.Description))
	b.WriteString("\n")

	crits := make([]string, 0, len(s.AcceptanceCriteria))
	for _, c := range s.AcceptanceCriteria {
		crits = append(crits, fmt.Sprintf("%s|%s|%s", c.ID, strings.TrimSpace(c.Description), strings.TrimSpace(c.Command)))
	}
	sort.Strings(crits)
	for _, c := range crits {
		b.WriteString("crit:")
		b.WriteString(c)
		b.WriteString("\n")
	}

	outs := make([]string, len(s.Outputs))
	copy(outs, s.Outputs)
	sort.Strings(outs)
	for _, o := range outs {
		b.WriteString("out:")
		b.WriteString(o)
		b.WriteString("\n")
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// writeUpstreamSessionMarker persists a marker for a converged session.
// changedFiles may be nil/empty for spec-only markers (preexisting case).
// provenance may be nil when caller doesn't have the context to fill it
// (preserves backward-compat with existing call sites).
func writeUpstreamSessionMarker(repoRoot string, session plan.Session, changedFiles []string, note string, provenance *SessionProvenance) error {
	if repoRoot == "" {
		return fmt.Errorf("writeUpstreamSessionMarker: empty repoRoot")
	}
	if err := os.MkdirAll(sessionMarkerDir(repoRoot), 0o755); err != nil {
		return fmt.Errorf("create marker dir: %w", err)
	}

	marker := SessionPersistMarker{
		SessionID:   session.ID,
		Title:       session.Title,
		SpecHash:    hashUpstreamSession(session),
		Files:       make(map[string]string, len(changedFiles)),
		CompletedAt: time.Now().Format(time.RFC3339),
		Note:        note,
		Provenance:  provenance,
	}
	for _, rel := range changedFiles {
		abs := filepath.Join(repoRoot, rel)
		data, err := os.ReadFile(abs)
		if err != nil {
			marker.Files[rel] = ""
			continue
		}
		sum := sha256.Sum256(data)
		marker.Files[rel] = hex.EncodeToString(sum[:])
	}

	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal marker: %w", err)
	}
	return os.WriteFile(sessionMarkerPath(repoRoot, session.ID), data, 0o644)
}

// isUpstreamSessionAlreadyComplete returns true if a marker exists for
// this session whose spec hash matches and (for file-tracked markers)
// every recorded file is still intact.
func isUpstreamSessionAlreadyComplete(repoRoot string, session plan.Session) (bool, string) {
	if repoRoot == "" {
		return false, "no repo root"
	}
	data, err := os.ReadFile(sessionMarkerPath(repoRoot, session.ID))
	if err != nil {
		return false, "no marker file"
	}
	var marker SessionPersistMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return false, fmt.Sprintf("marker corrupt: %v", err)
	}
	if marker.SpecHash != hashUpstreamSession(session) {
		return false, "session spec changed since last completion"
	}
	if len(marker.Files) == 0 {
		return true, "spec-only marker (spec hash matches)"
	}
	for rel, wantHash := range marker.Files {
		fileData, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			if wantHash == "" {
				continue
			}
			return false, fmt.Sprintf("file missing: %s", rel)
		}
		if wantHash == "" {
			return false, fmt.Sprintf("file should be absent but exists: %s", rel)
		}
		sum := sha256.Sum256(fileData)
		if hex.EncodeToString(sum[:]) != wantHash {
			return false, fmt.Sprintf("file modified since completion: %s", rel)
		}
	}
	return true, ""
}
