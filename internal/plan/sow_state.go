package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
)

// SOWState persists SOW execution progress so an interrupted or failed run
// can resume from where it left off. Stored under .stoke/sow-state.json
// relative to the project root.
//
// Resume semantics: a session whose Status == "done" and AcceptanceMet == true
// is skipped on the next run. Everything else is re-executed.
type SOWState struct {
	SOWID     string          `json:"sow_id"`
	SOWName   string          `json:"sow_name"`
	StartedAt time.Time       `json:"started_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Sessions  []SessionRecord `json:"sessions"`
}

// SessionRecord tracks one session's execution outcome.
type SessionRecord struct {
	SessionID       string              `json:"session_id"`
	Title           string              `json:"title"`
	Status          string              `json:"status"` // pending | running | done | failed | skipped | blocked
	AcceptanceMet   bool                `json:"acceptance_met"`
	Acceptance      []AcceptanceResult  `json:"acceptance,omitempty"`
	TaskResults     []TaskExecResult    `json:"task_results,omitempty"`
	Attempts        int                 `json:"attempts"`
	LastError       string              `json:"last_error,omitempty"`
	StartedAt       time.Time           `json:"started_at,omitempty"`
	FinishedAt      time.Time           `json:"finished_at,omitempty"`
}

// Session status values persisted to sow-state.json. They are part of
// the on-disk wire format — any change here must stay string-equal to
// what older states contain, so these are plain string constants.
const (
	sessionStatusPending  = "pending"
	sessionStatusRunning  = "running"
	sessionStatusDone     = "done"
	sessionStatusFailed   = "failed"
	sessionStatusSkipped  = "skipped"
	sessionStatusBlocked  = "blocked"
)

// SOWStatePath returns the canonical path for SOW state inside a project.
func SOWStatePath(projectRoot string) string {
	return r1dir.JoinFor(projectRoot, "sow-state.json")
}

// LoadSOWState reads SOW state from disk. Returns (nil, nil) if no state file
// exists yet — that's the normal case for a fresh build.
func LoadSOWState(projectRoot string) (*SOWState, error) {
	path := SOWStatePath(projectRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sow state: %w", err)
	}
	var st SOWState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse sow state: %w", err)
	}
	return &st, nil
}

// SaveSOWState writes SOW state atomically.
func SaveSOWState(projectRoot string, state *SOWState) error {
	path := SOWStatePath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir sow state: %w", err)
	}
	state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write sow state: %w", err)
	}
	return os.Rename(tmp, path)
}

// NewSOWState creates a fresh state object for a SOW.
func NewSOWState(sow *SOW) *SOWState {
	now := time.Now()
	st := &SOWState{
		SOWID:     sow.ID,
		SOWName:   sow.Name,
		StartedAt: now,
		UpdatedAt: now,
		Sessions:  make([]SessionRecord, 0, len(sow.Sessions)),
	}
	for _, s := range sow.Sessions {
		st.Sessions = append(st.Sessions, SessionRecord{
			SessionID: s.ID,
			Title:     s.Title,
			Status:    "pending",
		})
	}
	return st
}

// SessionByID finds a session record by ID. Returns nil if missing.
func (st *SOWState) SessionByID(id string) *SessionRecord {
	for i := range st.Sessions {
		if st.Sessions[i].SessionID == id {
			return &st.Sessions[i]
		}
	}
	return nil
}

// IsSessionComplete returns true if the session previously finished successfully
// (done status, acceptance met). Used by the scheduler to skip completed work
// on resume.
func (st *SOWState) IsSessionComplete(sessionID string) bool {
	rec := st.SessionByID(sessionID)
	if rec == nil {
		return false
	}
	return rec.Status == "done" && rec.AcceptanceMet
}

// MergeSOW reconciles a loaded state with a (possibly changed) SOW definition.
// If the SOW has new sessions not in state, they're appended as pending.
// If state has sessions not in the SOW, they're preserved but marked "skipped"
// so the user can see what used to be there. Session IDs are the join key.
func (st *SOWState) MergeSOW(sow *SOW) {
	known := map[string]bool{}
	for i := range st.Sessions {
		known[st.Sessions[i].SessionID] = true
	}
	for _, s := range sow.Sessions {
		if !known[s.ID] {
			st.Sessions = append(st.Sessions, SessionRecord{
				SessionID: s.ID,
				Title:     s.Title,
				Status:    "pending",
			})
		}
	}
	// Mark any state sessions no longer in the SOW as skipped (warning only).
	current := map[string]bool{}
	for _, s := range sow.Sessions {
		current[s.ID] = true
	}
	for i := range st.Sessions {
		if !current[st.Sessions[i].SessionID] && st.Sessions[i].Status == sessionStatusPending {
			st.Sessions[i].Status = sessionStatusSkipped
		}
	}
}

// RemainingSessions returns session IDs that still need to be executed.
func (st *SOWState) RemainingSessions() []string {
	var ids []string
	for _, s := range st.Sessions {
		if s.Status != sessionStatusDone || !s.AcceptanceMet {
			if s.Status == sessionStatusSkipped {
				continue
			}
			ids = append(ids, s.SessionID)
		}
	}
	return ids
}
