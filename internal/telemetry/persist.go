package telemetry

// persist.go — durable snapshots of a run's collected telemetry (O6).
//
// Before O6 the collector's Summary/Percentiles were computed in RAM and
// discarded at process exit: Orchestrator.Telemetry() had no production
// callers, so per-run latency percentiles, success rates, and per-
// category cost never reached disk. These helpers give the metrics a
// data path — cmd/r1 persists a Snapshot per run under
// <repo>/.r1/telemetry/<run-id>.json and `r1 stats telemetry` reads them
// back to flag week-over-week drift.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Snapshot is a serializable, point-in-time view of a Collector: the
// aggregate Summary plus latency percentiles for the "task" category
// (the run-lifecycle events Orchestrator.Run records). It is the on-disk
// schema shared by the writer (cmd/r1 run epilogues) and the reader
// (`r1 stats telemetry`), so both sides agree on the JSON shape.
type Snapshot struct {
	RunID           string                   `json:"run_id"`
	CapturedAt      time.Time                `json:"captured_at"`
	Summary         MetricsSummary           `json:"summary"`
	TaskPercentiles map[string]time.Duration `json:"task_percentiles,omitempty"`
}

// Snapshot builds a Snapshot from the collector's current state. The
// percentiles are taken for the "task" category — the category
// app.Orchestrator.Run uses for its task.start/task.end events.
func (c *Collector) Snapshot(runID string) Snapshot {
	return Snapshot{
		RunID:           runID,
		CapturedAt:      time.Now().UTC(),
		Summary:         c.Summary(),
		TaskPercentiles: c.Percentiles("task"),
	}
}

// WriteSnapshot persists snap to <dir>/<run-id>.json, creating dir when
// absent and writing atomically (tmp + rename) so a concurrent reader
// never observes a half-written file. The run-id is sanitized for
// filesystem safety. Returns the written path.
func WriteSnapshot(dir string, snap Snapshot) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, sanitizeRunID(snap.RunID)+".json")
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal snapshot: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".telemetry-*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return path, nil
}

// LoadSnapshot reads and decodes a single snapshot file.
func LoadSnapshot(path string) (Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// ListSnapshots loads every *.json snapshot in dir, sorted oldest to
// newest by CapturedAt (run-id as tiebreak). A missing dir yields an
// empty slice with no error so callers treat "no telemetry yet" as a
// normal state; corrupt files are skipped rather than failing the list.
func ListSnapshots(dir string) ([]Snapshot, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var snaps []Snapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		s, err := LoadSnapshot(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		snaps = append(snaps, s)
	}
	sort.Slice(snaps, func(i, j int) bool {
		if snaps[i].CapturedAt.Equal(snaps[j].CapturedAt) {
			return snaps[i].RunID < snaps[j].RunID
		}
		return snaps[i].CapturedAt.Before(snaps[j].CapturedAt)
	})
	return snaps, nil
}

// sanitizeRunID maps a run identifier to a filesystem-safe base name so
// a caller-supplied id (plan id, timestamp) can never traverse
// directories or collide with reserved characters.
func sanitizeRunID(id string) string {
	if strings.TrimSpace(id) == "" {
		return "run"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
