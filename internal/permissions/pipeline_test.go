package permissions

import (
	"testing"
)

func TestPipelineDenyTakesPriority(t *testing.T) {
	policy := Policy{
		Mode: ModeFull,
		DenyRules: []Rule{
			{Pattern: "Bash(git push*)", Reason: "push blocked"},
		},
		AllowRules: []Rule{
			{Pattern: "Bash"},
		},
	}
	p := NewPipeline(policy)

	// Denied command
	result := p.Authorize("Bash", "git push origin main", &Context{Mode: ModeFull})
	if result.Decision != Deny {
		t.Errorf("expected deny for git push, got %s", result.Decision)
	}

	// Allowed command
	result = p.Authorize("Bash", "npm test", &Context{Mode: ModeFull})
	if result.Decision != Allow {
		t.Errorf("expected allow for npm test, got %s", result.Decision)
	}
}

func TestPipelineHookOverride(t *testing.T) {
	policy := Policy{
		Mode: ModeWorkspaceWrite,
		AllowRules: []Rule{{Pattern: "Bash"}},
	}
	p := NewPipeline(policy)

	ctx := &Context{
		Mode:         ModeWorkspaceWrite,
		HookOverride: &Result{Decision: Deny, Reason: "hook says no"},
	}

	result := p.Authorize("Bash", "ls", ctx)
	if result.Decision != Deny {
		t.Errorf("expected deny from hook override, got %s", result.Decision)
	}
}

func TestPipelineAskRules(t *testing.T) {
	policy := Policy{
		Mode: ModeWorkspaceWrite,
		AskRules: []Rule{
			{Pattern: "Bash(sudo*)", Reason: "sudo requires confirmation"},
		},
		AllowRules: []Rule{{Pattern: "Bash"}},
	}
	p := NewPipeline(policy)

	result := p.Authorize("Bash", "sudo apt install", &Context{Mode: ModeWorkspaceWrite})
	if result.Decision != Ask {
		t.Errorf("expected ask for sudo, got %s", result.Decision)
	}
}

func TestPipelineReadOnlyMode(t *testing.T) {
	policy := Policy{Mode: ModeReadOnly}
	p := NewPipeline(policy)

	// Read allowed
	result := p.Authorize("Read", "/tmp/file", &Context{Mode: ModeReadOnly})
	if result.Decision != Allow {
		t.Errorf("expected allow for Read in read-only, got %s", result.Decision)
	}

	// Write denied
	result = p.Authorize("Write", "/tmp/file", &Context{Mode: ModeReadOnly})
	if result.Decision != Deny {
		t.Errorf("expected deny for Write in read-only, got %s", result.Decision)
	}
}

func TestPipelineDangerousMode(t *testing.T) {
	policy := Policy{Mode: ModeDangerous}
	p := NewPipeline(policy)

	result := p.Authorize("Bash", "rm -rf /", &Context{Mode: ModeDangerous})
	if result.Decision != Allow {
		t.Errorf("expected allow in dangerous mode, got %s", result.Decision)
	}
}

func TestMatchRule(t *testing.T) {
	tests := []struct {
		pattern, tool, input string
		want                 bool
	}{
		{"Bash", "Bash", "ls", true},
		{"Bash", "Read", "ls", false},
		{"Bash(git:*)", "Bash", "git push", true},
		{"Bash(git:*)", "Bash", "npm test", false},
		{"Write(.env*)", "Write", ".env.local", true},
		{"Write(.env*)", "Write", "main.go", false},
		{"*", "anything", "anything", true},
		{"Read", "Read", "/tmp/file", true},
	}

	for _, tc := range tests {
		got := matchRule(tc.pattern, tc.tool, tc.input)
		if got != tc.want {
			t.Errorf("matchRule(%q, %q, %q) = %v, want %v", tc.pattern, tc.tool, tc.input, got, tc.want)
		}
	}
}

func TestDefaultPolicy(t *testing.T) {
	policy := DefaultPolicy()
	p := NewPipeline(policy)

	// git push should be denied
	result := p.Authorize("Bash", "git push origin main", &Context{Mode: ModeWorkspaceWrite})
	if result.Decision != Deny {
		t.Errorf("expected deny for git push, got %s: %s", result.Decision, result.Reason)
	}

	// .env write should be denied
	result = p.Authorize("Write", ".env.local", &Context{Mode: ModeWorkspaceWrite})
	if result.Decision != Deny {
		t.Errorf("expected deny for .env write, got %s: %s", result.Decision, result.Reason)
	}

	// Normal read should be allowed
	result = p.Authorize("Read", "main.go", &Context{Mode: ModeWorkspaceWrite})
	if result.Decision != Allow {
		t.Errorf("expected allow for Read, got %s: %s", result.Decision, result.Reason)
	}

	// Normal write should be allowed
	result = p.Authorize("Write", "main.go", &Context{Mode: ModeWorkspaceWrite})
	if result.Decision != Allow {
		t.Errorf("expected allow for Write main.go, got %s: %s", result.Decision, result.Reason)
	}
}

func TestNilContext(t *testing.T) {
	policy := DefaultPolicy()
	p := NewPipeline(policy)

	// Should not panic with nil context
	result := p.Authorize("Read", "file.go", nil)
	if result.Decision != Allow {
		t.Errorf("expected allow, got %s", result.Decision)
	}
}
