package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseFlags_RequiresMission(t *testing.T) {
	_, err := parseFlags([]string{"--agent", "r1"})
	if err == nil {
		t.Fatalf("expected error for missing --mission")
	}
	if !strings.Contains(err.Error(), "mission") {
		t.Errorf("error should mention --mission: %v", err)
	}
}

func TestParseFlags_RequiresAgent(t *testing.T) {
	_, err := parseFlags([]string{"--mission", "x"})
	if err == nil {
		t.Fatalf("expected error for missing --agent")
	}
}

func TestParseFlags_HappyPath(t *testing.T) {
	f, err := parseFlags([]string{
		"--mission", "seed-hello-easy",
		"--agent", "r1",
		"--timeout", "1m",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if f.mission != "seed-hello-easy" {
		t.Errorf("mission = %q", f.mission)
	}
	if f.agent != "r1" {
		t.Errorf("agent = %q", f.agent)
	}
	if f.timeout != time.Minute {
		t.Errorf("timeout = %v, want 1m", f.timeout)
	}
}

func TestParseFlags_ListAgentsSkipsRequiredFlags(t *testing.T) {
	f, err := parseFlags([]string{"--list-agents"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !f.listAgents {
		t.Errorf("listAgents flag not set")
	}
}

func TestListAgentIDs_ContainsCoreAgents(t *testing.T) {
	ids := listAgentIDs()
	want := []string{"r1", "r1-antitrunc", "claude-code-default", "cline", "aider", "codex-cli", "cursor"}
	for _, w := range want {
		found := false
		for _, id := range ids {
			if id == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("listAgentIDs missing %q; got %v", w, ids)
		}
	}
}
