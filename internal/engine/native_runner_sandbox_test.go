package engine

// Sandbox wiring tests for the native runner (see internal/sandbox).
//
// Coverage:
//   - SandboxEnabled + unenforceable backend fails closed BEFORE any
//     provider call (no model turn, no tool dispatch).
//   - R1_NATIVE_SANDBOX=off kill switch restores the unsandboxed path.
//   - SandboxEnabled=false never consults the sandbox layer (legacy
//     behavior preserved).
//
// All tests force deterministic backend availability (explicit mode +
// stripped PATH) so they pass identically on hosts with and without
// bwrap/Landlock, offline, with no credentials.

import (
	"context"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// tripwireProvider fails the test if the model is ever consulted.
type tripwireProvider struct{ t *testing.T }

func (p *tripwireProvider) Name() string { return "tripwire" }

func (p *tripwireProvider) Chat(provider.ChatRequest) (*provider.ChatResponse, error) {
	p.t.Error("provider called: sandbox fail-closed must abort before any model turn")
	return &provider.ChatResponse{StopReason: "end_turn"}, nil
}

func (p *tripwireProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return p.Chat(req)
}

func TestNativeRunner_SandboxFailClosed(t *testing.T) {
	t.Setenv("R1_NATIVE_SANDBOX", "bwrap")
	t.Setenv("STOKE_NATIVE_SANDBOX", "")
	t.Setenv("PATH", t.TempDir()) // no bwrap binary anywhere

	runner := NewNativeRunner("", "claude-sonnet-4-5")
	runner.ProviderOverride = &tripwireProvider{t: t}
	spec := newMinimalRunSpec(t)
	spec.SandboxEnabled = true

	res, err := runner.Run(context.Background(), spec, nil)
	if err == nil {
		t.Fatal("want fail-closed error when requested sandbox is unenforceable")
	}
	if !res.IsError {
		t.Error("RunResult.IsError must be set on sandbox refusal")
	}
	for _, sub := range []string{"cannot be enforced", "R1_NATIVE_SANDBOX=off", spec.Phase.Name} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error %q missing %q", err, sub)
		}
	}
}

func TestNativeRunner_SandboxKillSwitch(t *testing.T) {
	t.Setenv("R1_NATIVE_SANDBOX", "off")
	t.Setenv("STOKE_NATIVE_SANDBOX", "")

	runner := NewNativeRunner("", "claude-sonnet-4-5")
	spec := newMinimalRunSpec(t)
	spec.SandboxEnabled = true

	fp := &fakeMCPProvider{
		responses: []*provider.ChatResponse{{
			Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		}},
	}
	if _, err := runWithProvider(t, runner, spec, fp); err != nil {
		t.Fatalf("kill switch must restore the unsandboxed path: %v", err)
	}
}

func TestNativeRunner_SandboxNotRequestedIgnoresEnv(t *testing.T) {
	// Even a hard-broken sandbox config must not matter when the spec
	// never asked for one (plan phase, legacy callers).
	t.Setenv("R1_NATIVE_SANDBOX", "bwarp-typo")

	runner := NewNativeRunner("", "claude-sonnet-4-5")
	spec := newMinimalRunSpec(t)
	spec.SandboxEnabled = false

	fp := &fakeMCPProvider{
		responses: []*provider.ChatResponse{{
			Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		}},
	}
	if _, err := runWithProvider(t, runner, spec, fp); err != nil {
		t.Fatalf("unsandboxed spec must ignore sandbox env: %v", err)
	}
}
