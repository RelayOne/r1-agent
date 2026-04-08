package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Fork represents a branched session from a parent.
// Inspired by claw-code-parity's SessionFork: allows creating branched
// conversations from a parent session for exploration or parallel work.
type Fork struct {
	ID              string    `json:"id"`
	ParentSessionID string    `json:"parent_session_id"`
	BranchName      string    `json:"branch_name"`
	Description     string    `json:"description"`
	CreatedAt       time.Time `json:"created_at"`
	State           string    `json:"state"` // active, merged, abandoned
}

// ForkSession creates a new branched session from the current state.
// The fork gets a copy of the current session state but tracks independently.
func (s *Store) ForkSession(parentID, branchName, description string) (*Fork, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fork := &Fork{
		ID:              fmt.Sprintf("%s-fork-%d", parentID, time.Now().UnixNano()),
		ParentSessionID: parentID,
		BranchName:      branchName,
		Description:     description,
		CreatedAt:       time.Now(),
		State:           "active",
	}

	// Save fork metadata
	forksDir := filepath.Join(s.root, "forks")
	if err := os.MkdirAll(forksDir, 0700); err != nil {
		return nil, fmt.Errorf("create forks dir: %w", err)
	}

	data, err := json.MarshalIndent(fork, "", "  ")
	if err != nil {
		return nil, err
	}

	forkPath := filepath.Join(forksDir, fork.ID+".json")
	if err := os.WriteFile(forkPath, data, 0644); err != nil {
		return nil, err
	}

	// Copy current session state to fork
	parentState, _ := s.LoadState()
	if parentState != nil {
		forkState := *parentState
		forkState.PlanID = fork.ID
		forkData, _ := json.MarshalIndent(&forkState, "", "  ")
		statePath := filepath.Join(forksDir, fork.ID+"-state.json")
		os.WriteFile(statePath, forkData, 0644)
	}

	return fork, nil
}

// ListForks returns all forks for a session.
func (s *Store) ListForks() ([]Fork, error) {
	forksDir := filepath.Join(s.root, "forks")
	entries, err := os.ReadDir(forksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var forks []Fork
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		// Skip state files
		name := entry.Name()
		if len(name) > 11 && name[len(name)-11:] == "-state.json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(forksDir, name))
		if err != nil {
			continue
		}
		var fork Fork
		if json.Unmarshal(data, &fork) == nil {
			forks = append(forks, fork)
		}
	}
	return forks, nil
}

// LoadForkState loads the session state for a fork.
func (s *Store) LoadForkState(forkID string) (*State, error) {
	statePath := filepath.Join(s.root, "forks", forkID+"-state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// MergeFork marks a fork as merged.
func (s *Store) MergeFork(forkID string) error {
	return s.updateForkState(forkID, "merged")
}

// AbandonFork marks a fork as abandoned.
func (s *Store) AbandonFork(forkID string) error {
	return s.updateForkState(forkID, "abandoned")
}

func (s *Store) updateForkState(forkID, state string) error {
	forkPath := filepath.Join(s.root, "forks", forkID+".json")
	data, err := os.ReadFile(forkPath)
	if err != nil {
		return err
	}
	var fork Fork
	if err := json.Unmarshal(data, &fork); err != nil {
		return err
	}
	fork.State = state
	updated, err := json.MarshalIndent(&fork, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(forkPath, updated, 0644)
}
