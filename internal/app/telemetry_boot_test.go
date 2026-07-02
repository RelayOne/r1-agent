package app

// Tests for telemetry auto-construction (audit A096): RunConfig.Telemetry
// used to be documented "nil = disabled" while no production path ever
// constructed a collector, making the app.Run Record calls unreachable.

import (
	"testing"

	"github.com/RelayOne/r1/internal/taskstate"
	"github.com/RelayOne/r1/internal/telemetry"
)

func TestNewAutoConstructsTelemetry(t *testing.T) {
	o, err := New(RunConfig{
		RepoRoot: t.TempDir(),
		Task:     "test task",
		AuthMode: AuthModeMode1,
		State:    taskstate.NewTaskState("test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.Telemetry() == nil {
		t.Fatal("New must auto-construct a telemetry collector when none is injected")
	}
	if o.Telemetry().EventCount() != 0 {
		t.Errorf("fresh collector should have 0 events, got %d", o.Telemetry().EventCount())
	}
}

func TestNewHonorsInjectedTelemetry(t *testing.T) {
	injected := telemetry.New()
	injected.Record(telemetry.Event{Name: "pre-existing", Category: "test", Success: true})

	o, err := New(RunConfig{
		RepoRoot:  t.TempDir(),
		Task:      "test task",
		AuthMode:  AuthModeMode1,
		State:     taskstate.NewTaskState("test"),
		Telemetry: injected,
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.Telemetry() != injected {
		t.Fatal("New must keep an injected collector (aggregation across runs)")
	}
	if o.Telemetry().EventCount() != 1 {
		t.Errorf("injected collector should keep its events, got %d", o.Telemetry().EventCount())
	}
}
