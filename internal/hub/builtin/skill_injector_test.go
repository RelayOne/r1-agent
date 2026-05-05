package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
	"github.com/RelayOne/r1/internal/skill"
)

func TestSkillInjector_EmitsSkillLoadedNodes(t *testing.T) {
	tmp := t.TempDir()
	led, err := ledger.New(filepath.Join(tmp, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}

	skillDir := filepath.Join(tmp, "skills")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	for _, k := range []string{"alpha", "beta", "gamma"} {
		body := "# " + k + "\n<!-- keywords: " + k + " -->\nContent for " + k + ".\n\n## Gotchas\n\nWatch out for things in " + k + "."
		if err := os.WriteFile(filepath.Join(skillDir, k+".md"), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s.md: %v", k, err)
		}
	}
	reg := skill.NewRegistry(skillDir)
	if err := reg.Load(); err != nil {
		t.Fatalf("registry Load: %v", err)
	}

	inj := &SkillInjector{
		Registry:    reg,
		TokenBudget: 6000,
		Ledger:      led,
	}

	ev := &hub.Event{
		Type:    hub.EventPromptSkillsMatched,
		AgentID: "stance-cto-1",
		TaskID:  "task-007",
		Prompt:  &hub.PromptEvent{},
		Custom: map[string]any{
			"prompt":                  "alpha beta gamma — do the work",
			"stance_role":             "cto",
			"concern_field_template":  "cto_planning",
			"loop_id":                 "loop-42",
		},
		Timestamp: time.Now(),
	}

	resp := inj.handle(context.Background(), ev)
	if resp == nil || resp.Decision != hub.Allow {
		t.Fatalf("handle: unexpected response %+v", resp)
	}

	loaded := readLedgerSkillLoaded(t, filepath.Join(tmp, "ledger"))
	if len(loaded) == 0 {
		t.Fatal("no skill_loaded ledger nodes were emitted")
	}
	for _, sl := range loaded {
		if sl.LoadingStanceID != "stance-cto-1" {
			t.Errorf("LoadingStanceID = %q, want stance-cto-1", sl.LoadingStanceID)
		}
		if sl.LoadingStanceRole != "cto" {
			t.Errorf("LoadingStanceRole = %q, want cto", sl.LoadingStanceRole)
		}
		if sl.ConcernFieldTemplate != "cto_planning" {
			t.Errorf("ConcernFieldTemplate = %q, want cto_planning", sl.ConcernFieldTemplate)
		}
		if sl.TaskDAGScope != "task-007" {
			t.Errorf("TaskDAGScope = %q, want task-007", sl.TaskDAGScope)
		}
		if sl.LoopRef != "loop-42" {
			t.Errorf("LoopRef = %q, want loop-42", sl.LoopRef)
		}
		if sl.SkillRef == "" {
			t.Error("SkillRef must not be empty")
		}
	}
}

// readLedgerSkillLoaded walks the chain tier to find skill_loaded
// nodes, then reads each one's content tier blob to unpack the
// SkillLoaded payload. Two-tier layout per ledger T6: chain tier
// holds metadata + commitment, content tier holds salt + canonical
// content body.
func readLedgerSkillLoaded(t *testing.T, ledgerRoot string) []nodes.SkillLoaded {
	t.Helper()
	out := []nodes.SkillLoaded{}
	chainDir := filepath.Join(ledgerRoot, "chain")
	contentDir := filepath.Join(ledgerRoot, "content")
	entries, err := os.ReadDir(chainDir)
	if err != nil {
		t.Fatalf("read chain dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(chainDir, e.Name()))
		if err != nil {
			t.Fatalf("read chain %s: %v", e.Name(), err)
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &head); err != nil || head.Type != "skill_loaded" {
			continue
		}
		// Content tier file path: <root>/content/<node-id>.json
		contentRaw, err := os.ReadFile(filepath.Join(contentDir, e.Name()))
		if err != nil {
			t.Errorf("read content %s: %v", e.Name(), err)
			continue
		}
		// Content tier wraps payload as {"salt":"...","content":...}
		var wrap struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(contentRaw, &wrap); err != nil {
			t.Errorf("unmarshal content wrap: %v", err)
			continue
		}
		body := wrap.Content
		if len(body) == 0 {
			body = contentRaw // older layout had raw content directly
		}
		var sl nodes.SkillLoaded
		if err := json.Unmarshal(body, &sl); err != nil {
			t.Errorf("unmarshal SkillLoaded body: %v\nraw=%s", err, string(body))
			continue
		}
		out = append(out, sl)
	}
	return out
}

func TestSkillInjector_DisabledWhenLedgerNil(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "skills")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "foo.md"),
		[]byte("# foo\n<!-- keywords: foo -->\n\nstuff\n\n## Gotchas\n\nWatch out."), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	reg := skill.NewRegistry(skillDir)
	if err := reg.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	inj := &SkillInjector{Registry: reg, TokenBudget: 3000, Ledger: nil}
	ev := &hub.Event{
		Type:   hub.EventPromptSkillsMatched,
		Prompt: &hub.PromptEvent{},
		Custom: map[string]any{"prompt": "foo bar"},
	}
	resp := inj.handle(context.Background(), ev)
	if resp == nil || resp.Decision != hub.Allow {
		t.Fatalf("handle: %+v", resp)
	}
	// No ledger → no nodes written. Indirect assertion: handle did
	// not panic when emit path was skipped.
	if md, _ := resp.Metadata["skill_count"].(int); md == 0 {
		t.Errorf("expected ≥1 skill match for prompt 'foo bar'")
	}
}
