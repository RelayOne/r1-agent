package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/taskstate"
)

// writeGovernanceEnabledPolicy writes a minimal valid policy YAML with the
// governance block explicitly enabled and returns its path. This makes
// policy.Governance.Enabled == true so the construction gate's policy term
// is satisfied, isolating the GovernanceDisabled kill-switch under test.
func writeGovernanceEnabledPolicy(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "stoke.yaml")
	body := "governance:\n  enabled: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	return path
}

// TestGovernanceGate verifies the kill-switch + construction gate in app.New:
//
//	governor constructed IFF EventBus != nil && !GovernanceDisabled &&
//	                         (GovernanceEnabled || policy.Governance.Enabled)
//
// Each subtest uses a minimal RunConfig with a non-nil EventBus (hub.New()),
// a temp RepoRoot, and a policy whose Governance.Enabled is true.
func TestGovernanceGate(t *testing.T) {
	// (i) kill-switch wins over a default-on policy: GovernanceDisabled=true
	//     + policy Governance.Enabled=true => governor == nil.
	t.Run("DisabledOverridesPolicy", func(t *testing.T) {
		dir := t.TempDir()
		o, err := New(RunConfig{
			RepoRoot:           dir,
			PolicyPath:         writeGovernanceEnabledPolicy(t, dir),
			Task:               "test",
			AuthMode:           AuthModeMode1,
			State:              taskstate.NewTaskState("test"),
			EventBus:           hub.New(),
			GovernanceDisabled: true,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if o.governor != nil {
			t.Fatal("governor != nil: kill-switch should prevent construction despite policy default-on")
		}
	})

	// (ii) policy default-on with the flag off and no kill-switch:
	//      GovernanceEnabled=false + policy Governance.Enabled=true =>
	//      governor != nil.
	t.Run("PolicyEnablesGovernor", func(t *testing.T) {
		dir := t.TempDir()
		o, err := New(RunConfig{
			RepoRoot:          dir,
			PolicyPath:        writeGovernanceEnabledPolicy(t, dir),
			Task:              "test",
			AuthMode:          AuthModeMode1,
			State:             taskstate.NewTaskState("test"),
			EventBus:          hub.New(),
			GovernanceEnabled: false,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if o.governor == nil {
			t.Fatal("governor == nil: policy Governance.Enabled=true should construct a governor")
		}
	})

	// (iii) kill-switch wins over an explicit flag: GovernanceDisabled=true
	//       + GovernanceEnabled=true => governor == nil.
	t.Run("DisabledOverridesFlag", func(t *testing.T) {
		dir := t.TempDir()
		o, err := New(RunConfig{
			RepoRoot:           dir,
			PolicyPath:         writeGovernanceEnabledPolicy(t, dir),
			Task:               "test",
			AuthMode:           AuthModeMode1,
			State:              taskstate.NewTaskState("test"),
			EventBus:           hub.New(),
			GovernanceEnabled:  true,
			GovernanceDisabled: true,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if o.governor != nil {
			t.Fatal("governor != nil: kill-switch should win over GovernanceEnabled=true")
		}
	})
}
