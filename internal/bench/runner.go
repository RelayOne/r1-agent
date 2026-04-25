package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/concern"
	"github.com/RelayOne/r1-agent/internal/harness"
	"github.com/RelayOne/r1-agent/internal/ledger"
	"gopkg.in/yaml.v3"
)

// Runner executes golden missions against the Stoke substrate.
type Runner struct {
	goldenDir string
}

// NewRunner creates a Runner that loads golden missions from goldenDir.
func NewRunner(goldenDir string) *Runner {
	return &Runner{goldenDir: goldenDir}
}

// LoadMission loads a golden mission config by ID (directory name).
func (r *Runner) LoadMission(missionID string) (*MissionConfig, error) {
	path := filepath.Join(r.goldenDir, missionID, "mission.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("bench: load mission %q: %w", missionID, err)
	}
	var cfg MissionConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("bench: parse mission %q: %w", missionID, err)
	}
	return &cfg, nil
}

// ListMissions returns all available golden missions by scanning goldenDir.
func (r *Runner) ListMissions() ([]MissionConfig, error) {
	entries, err := os.ReadDir(r.goldenDir)
	if err != nil {
		return nil, fmt.Errorf("bench: list missions: %w", err)
	}

	var missions []MissionConfig
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cfg, err := r.LoadMission(e.Name())
		if err != nil {
			continue // skip malformed missions
		}
		missions = append(missions, *cfg)
	}
	return missions, nil
}

// Run executes a single mission in an isolated temp directory with a fresh
// ledger, bus, and harness, then collects metrics.
func (r *Runner) Run(ctx context.Context, mission *MissionConfig) (*RunResult, error) {
	tmpDir, err := os.MkdirTemp("", "bench-"+mission.ID+"-")
	if err != nil {
		return nil, fmt.Errorf("bench: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	ledgerDir := filepath.Join(tmpDir, "ledger")
	busDir := filepath.Join(tmpDir, "bus")

	l, err := ledger.New(ledgerDir)
	if err != nil {
		return nil, fmt.Errorf("bench: create ledger: %w", err)
	}
	defer l.Close()

	b, err := bus.New(busDir)
	if err != nil {
		return nil, fmt.Errorf("bench: create bus: %w", err)
	}
	defer b.Close()

	cb := concern.NewBuilder(l, b)

	h := harness.New(harness.Config{
		MissionID:     mission.ID,
		DefaultModel:  "claude-opus-4-6",
		OperatingMode: "full_auto",
	}, l, b, cb)

	start := time.Now()

	// Publish mission.started event.
	payload, _ := json.Marshal(map[string]string{
		"title":    mission.Title,
		"category": mission.Category,
	})
	_ = b.Publish(bus.Event{
		Type:      bus.EvtMissionStarted,
		EmitterID: "bench-runner",
		Scope:     bus.Scope{MissionID: mission.ID},
		Payload:   payload,
	})

	// Spawn a dev stance to execute the mission.
	_, err = h.SpawnStance(ctx, harness.SpawnRequest{
		Role:         "dev",
		TaskDAGScope: mission.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("bench: spawn stance: %w", err)
	}

	// Publish mission.completed event.
	_ = b.Publish(bus.Event{
		Type:      bus.EvtMissionCompleted,
		EmitterID: "bench-runner",
		Scope:     bus.Scope{MissionID: mission.ID},
	})

	wallMs := time.Since(start).Milliseconds()

	result, err := ComputeMetrics(ctx, l, b, mission.ID)
	if err != nil {
		return nil, fmt.Errorf("bench: compute metrics: %w", err)
	}
	result.MissionID = mission.ID
	result.WallTimeMs = wallMs
	result.AcceptanceTotal = len(mission.Acceptance)

	// Verify ledger integrity; corruption is a bench failure.
	if verr := l.Verify(ctx); verr != nil {
		result.LedgerCorrupted = true
	}

	return result, nil
}

// RunAll executes all golden missions and returns their results.
func (r *Runner) RunAll(ctx context.Context) ([]RunResult, error) {
	missions, err := r.ListMissions()
	if err != nil {
		return nil, err
	}

	var results []RunResult
	for i := range missions {
		res, err := r.Run(ctx, &missions[i])
		if err != nil {
			return nil, fmt.Errorf("bench: run %q: %w", missions[i].ID, err)
		}
		results = append(results, *res)
	}
	return results, nil
}
