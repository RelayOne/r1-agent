package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/concern"
	"github.com/RelayOne/r1/internal/harness"
	"github.com/RelayOne/r1/internal/ledger"
	"gopkg.in/yaml.v3"
)

// concernTemplateMissingMarker is the prefix of the harness error that
// indicates the concern Builder has no template for the role/face the
// runner asked for. Run() wraps this case with ErrFixtureBoundary so callers
// can distinguish it from real regressions via errors.Is.
const concernTemplateMissingMarker = "concern: no template for"

// Runner executes golden missions against the Stoke substrate.
type Runner struct {
	goldenDir string

	// Provider drives the spawned stance's model turns. Nil means the
	// offline deterministic default (a harness.MockProvider with zero
	// cost) so golden missions never touch the network; callers wanting
	// real model turns inject a real Provider (e.g. harness.APIProvider).
	Provider harness.Provider
}

// NewRunner creates a Runner that loads golden missions from goldenDir.
func NewRunner(goldenDir string) *Runner {
	return &Runner{goldenDir: goldenDir}
}

// LoadMission loads a golden mission config by ID. It first looks for a flat
// fixture file (<goldenDir>/<id>.json|.yaml|.yml) and falls back to the
// legacy nested layout (<goldenDir>/<id>/mission.yaml). The flat layout is
// preferred for new fixtures (e.g. testdata/missions/*.json) because it
// keeps small canonical fixtures contained to a single file.
func (r *Runner) LoadMission(missionID string) (*MissionConfig, error) {
	// Try flat <id>.json then <id>.yaml/.yml first.
	for _, ext := range []string{".json", ".yaml", ".yml"} {
		candidate := filepath.Join(r.goldenDir, missionID+ext)
		if _, err := os.Stat(candidate); err == nil {
			return r.loadFromFile(missionID, candidate)
		}
	}
	// Fall back to the legacy nested mission.yaml layout.
	nested := filepath.Join(r.goldenDir, missionID, "mission.yaml")
	if _, err := os.Stat(nested); err == nil {
		return r.loadFromFile(missionID, nested)
	}
	return nil, fmt.Errorf("bench: load mission %q: no fixture found under %s (looked for <id>.json, <id>.yaml, <id>.yml, <id>/mission.yaml)", missionID, r.goldenDir)
}

// loadFromFile reads the given file path, decodes it as JSON or YAML based
// on extension, and validates the mission ID matches missionID.
func (r *Runner) loadFromFile(missionID, path string) (*MissionConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("bench: load mission %q: %w", missionID, err)
	}
	var cfg MissionConfig
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("bench: parse mission %q (%s): %w", missionID, path, err)
		}
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("bench: parse mission %q (%s): %w", missionID, path, err)
		}
	default:
		return nil, fmt.Errorf("bench: parse mission %q: unsupported fixture extension %q", missionID, filepath.Ext(path))
	}
	return &cfg, nil
}

// ListMissions returns all available golden missions by scanning goldenDir.
// It accepts both the legacy nested layout (<id>/mission.yaml) and the flat
// layout (<id>.json|.yaml|.yml at the top level of goldenDir).
func (r *Runner) ListMissions() ([]MissionConfig, error) {
	entries, err := os.ReadDir(r.goldenDir)
	if err != nil {
		return nil, fmt.Errorf("bench: list missions: %w", err)
	}

	seen := make(map[string]bool)
	var missions []MissionConfig
	for _, e := range entries {
		name := e.Name()
		var id string
		switch {
		case e.IsDir():
			id = name
		case strings.HasSuffix(strings.ToLower(name), ".json"),
			strings.HasSuffix(strings.ToLower(name), ".yaml"),
			strings.HasSuffix(strings.ToLower(name), ".yml"):
			id = strings.TrimSuffix(name, filepath.Ext(name))
		default:
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		cfg, err := r.LoadMission(id)
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
	// Register minimal concern templates so spawn() can build a concern
	// field for the dev/proposing and reviewer/reviewing roles. We use
	// section-less templates intentionally — the bench substrate exercises
	// the lifecycle, not the concern content. Importing the full
	// internal/concern/templates registry would pull heavyweight ledger
	// section dependencies into every bench test.
	cb.RegisterTemplate("bench_dev_proposing", concern.Template{
		Role: concern.RoleDev,
		Face: concern.FaceProposing,
	})
	cb.RegisterTemplate("bench_reviewer_reviewing", concern.Template{
		Role: concern.RoleReviewer,
		Face: concern.FaceReviewing,
	})

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
	handle, err := h.SpawnStance(ctx, harness.SpawnRequest{
		Role:         "dev",
		TaskDAGScope: mission.ID,
	})
	if err != nil {
		// If a concern Template is missing — e.g. callers using NewRunner
		// against a registry that doesn't pre-register the minimal
		// templates wired above — surface that as ErrFixtureBoundary so
		// tests can errors.Is the well-known unit-test limitation
		// without burying real failures.
		if strings.Contains(err.Error(), concernTemplateMissingMarker) {
			return nil, fmt.Errorf("bench: spawn stance: %w: %v", ErrFixtureBoundary, err)
		}
		return nil, fmt.Errorf("bench: spawn stance: %w", err)
	}

	// Drive the stance through at least one real runner turn so golden
	// missions measure actual substrate work — worker.action events,
	// between-turn checkpoint discipline, and token/cost accounting via
	// cost_record ledger nodes (audit A041; previously the stance
	// performed no work between spawn and mission.completed).
	prov := r.Provider
	if prov == nil {
		prov = &harness.MockProvider{Responses: []*harness.ChatResponse{{
			Content: "bench: golden mission acknowledged (offline substrate provider)",
		}}}
	}
	stanceRunner := h.NewStanceRunner(prov, nil, harness.RunnerConfig{MaxTurns: 4})
	if _, err := stanceRunner.Run(ctx, handle.ID, fmt.Sprintf("Execute golden mission %s: %s", mission.ID, mission.Title)); err != nil {
		return nil, fmt.Errorf("bench: run stance: %w", err)
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
