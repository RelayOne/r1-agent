package app

import (
	"testing"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// stubJudge is a minimal provider.Provider for wiring tests.
type stubJudge struct{}

func (stubJudge) Name() string { return "stub" }
func (stubJudge) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{}, nil
}
func (stubJudge) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{}, nil
}

// TestRegisterHonestySubscribers_Disabled: enabled:false registers nothing.
func TestRegisterHonestySubscribers_Disabled(t *testing.T) {
	bus := hub.New()
	registerHonestySubscribers(bus, config.HonestyConfig{}, nil, "")
	if got := bus.SubscriberCount(); got != 0 {
		t.Errorf("disabled honesty registered %d subscribers, want 0", got)
	}
}

// TestRegisterHonestySubscribers_DefaultConfig: the default config
// (Enabled + CheckImports + CoTMonitoring, no judge) registers the four
// deterministic subscribers: HonestyGate, TestIntegrityChecker,
// ImportChecker, CoTMonitor.
func TestRegisterHonestySubscribers_DefaultConfig(t *testing.T) {
	bus := hub.New()
	registerHonestySubscribers(bus, config.DefaultHonestyConfig(), nil, "")
	if got := bus.SubscriberCount(); got != 4 {
		t.Errorf("default honesty registered %d subscribers, want 4", got)
	}
}

// TestRegisterHonestySubscribers_LLMFlagsNeedJudge: claim_decomposition
// and confession register only when a judge provider is supplied.
func TestRegisterHonestySubscribers_LLMFlagsNeedJudge(t *testing.T) {
	hc := config.HonestyConfig{
		Enabled:            true,
		ClaimDecomposition: true,
		Confession:         true,
	}

	noJudge := hub.New()
	registerHonestySubscribers(noJudge, hc, nil, "")
	if got := noJudge.SubscriberCount(); got != 2 { // gate + test integrity only
		t.Errorf("no judge: registered %d subscribers, want 2", got)
	}

	withJudge := hub.New()
	registerHonestySubscribers(withJudge, hc, stubJudge{}, "claude-haiku-4-5")
	if got := withJudge.SubscriberCount(); got != 4 { // + decomposer + confession
		t.Errorf("with judge: registered %d subscribers, want 4", got)
	}
}

// TestRegisterHonestySubscribers_NilBus must not panic.
func TestRegisterHonestySubscribers_NilBus(t *testing.T) {
	registerHonestySubscribers(nil, config.DefaultHonestyConfig(), nil, "")
}
