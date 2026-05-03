package memorycurator

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/provider"
)

// makeRememberToolUse builds a "remember" tool_use ResponseContent block
// carrying the supplied category + content. Helper for the curate tests.
func makeRememberToolUse(id, category, content string) provider.ResponseContent {
	return provider.ResponseContent{
		Type: "tool_use",
		Name: rememberToolName,
		ID:   id,
		Input: map[string]any{
			"category": category,
			"content":  content,
		},
	}
}

// runUntilTrigger drives Run repeatedly until TriggerCount advances or
// timeout elapses. Returns true on advance, false on timeout. Helper
// for the privacy / audit / per-category tests so they can assert the
// Lobe behaviour without sleeping a fixed wall-clock interval.
func runUntilTrigger(t *testing.T, l *MemoryCuratorLobe, history []agentloop.Message) {
	t.Helper()
	in := cortex.LobeInput{History: history}
	// Drive 5 ticks: the every-5-turns predicate guarantees one fire.
	for i := 0; i < curatorTurnInterval; i++ {
		if err := l.Run(context.Background(), in); err != nil {
			t.Fatalf("Run(%d): %v", i+1, err)
		}
	}
}

// TestMemoryCuratorLobe_SkipsPrivateMessages covers the privacy half of
// TASK-30: a tail containing any message with the privateTagSentinel
// must skip the entire haikuCall — the provider is never invoked, no
// memory write happens, no audit entry is written, no Note is
// published.
func TestMemoryCuratorLobe_SkipsPrivateMessages(t *testing.T) {
	t.Parallel()

	fp := &fakeProvider{
		content: []provider.ResponseContent{
			makeRememberToolUse("tu-1", "fact", "this should never be persisted"),
		},
	}
	l, ws, _ := newCuratorForTest(t, fp)

	history := []agentloop.Message{
		{
			Role: "user",
			Content: []agentloop.ContentBlock{
				{Type: "text", Text: privateTagSentinel + " here is a secret recipe"},
			},
		},
		{
			Role: "assistant",
			Content: []agentloop.ContentBlock{
				{Type: "text", Text: "noted"},
			},
		},
	}
	runUntilTrigger(t, l, history)

	if got := fp.callCount.Load(); got != 0 {
		t.Errorf("fakeProvider.callCount = %d, want 0 (privacy gate must skip the call)", got)
	}
	if got := len(ws.Snapshot()); got != 0 {
		t.Errorf("Workspace.Snapshot() = %d notes, want 0", got)
	}

	// The audit log should not exist because no auto-write happened.
	if _, err := os.Stat(l.privacy.AuditLogPath); !os.IsNotExist(err) {
		t.Errorf("audit log file should not exist after privacy skip, stat err=%v", err)
	}
}

// TestMemoryCuratorLobe_AutoAppliesOnlyConfiguredCategories covers the
// per-category half of TASK-30: with AutoCurateCategories={fact}, a
// model response containing one fact + one preference + one gotcha
// produces exactly one memory.Store write (the fact). The other two
// candidates are queued as confirm-Notes with tag="memory-confirm".
func TestMemoryCuratorLobe_AutoAppliesOnlyConfiguredCategories(t *testing.T) {
	t.Parallel()

	fp := &fakeProvider{
		content: []provider.ResponseContent{
			makeRememberToolUse("tu-1", "fact", "build with go build ./cmd/r1"),
			makeRememberToolUse("tu-2", "preference", "user prefers tabs over spaces"),
			makeRememberToolUse("tu-3", "gotcha", "git submodules need init --recursive"),
		},
	}
	l, ws, _ := newCuratorForTest(t, fp)

	history := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "how do I build?"}}},
	}
	runUntilTrigger(t, l, history)

	// Wait for the workspace publishes (SevInfo Notes are processed
	// synchronously via Publish; subscribers fire async but Snapshot
	// reflects the post-Publish state immediately).
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(ws.Snapshot()) >= 3 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	notes := ws.Snapshot()
	if got, want := len(notes), 3; got != want {
		t.Fatalf("len(Notes) = %d, want %d (1 auto + 2 confirm); have: %+v", got, want, notes)
	}

	var autoCount, confirmCount int
	for _, n := range notes {
		switch {
		case containsTag(n.Tags, "memory") && !containsTag(n.Tags, "memory-confirm"):
			autoCount++
		case containsTag(n.Tags, "memory-confirm"):
			confirmCount++
		}
	}
	if autoCount != 1 {
		t.Errorf("autoCount = %d, want 1 (the fact)", autoCount)
	}
	if confirmCount != 2 {
		t.Errorf("confirmCount = %d, want 2 (preference + gotcha)", confirmCount)
	}

	// Memory store must contain exactly the fact.
	allFacts := l.mem.RecallByCategory(memory.CatFact)
	if got, want := len(allFacts), 1; got != want {
		t.Errorf("memory.RecallByCategory(fact) = %d entries, want %d", got, want)
	}
	if len(allFacts) > 0 {
		if got, want := allFacts[0].Content, "build with go build ./cmd/r1"; got != want {
			t.Errorf("memory entry content = %q, want %q", got, want)
		}
	}

	// Memory store must NOT contain the preference / gotcha (they were
	// queued, not auto-applied).
	if got := len(l.mem.RecallByCategory(memory.CatPreference)); got != 0 {
		t.Errorf("memory.RecallByCategory(preference) = %d, want 0", got)
	}
	if got := len(l.mem.RecallByCategory(memory.CatGotcha)); got != 0 {
		t.Errorf("memory.RecallByCategory(gotcha) = %d, want 0", got)
	}
}

// TestMemoryCuratorLobe_AppendsAuditLog covers the audit-log half of
// TASK-30: every auto-write produces one JSONL line in
// PrivacyConfig.AuditLogPath. Two consecutive trigger fires with one
// fact each must produce exactly two lines, in order, with the
// declared schema.
func TestMemoryCuratorLobe_AppendsAuditLog(t *testing.T) {
	t.Parallel()

	fp := &fakeProvider{
		content: []provider.ResponseContent{
			makeRememberToolUse("tu-1", "fact", "deploy target is gcp/us-central1"),
		},
	}
	l, _, _ := newCuratorForTest(t, fp)

	// First trigger: fires after 5 Run() ticks.
	history := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "hi"}}},
	}
	runUntilTrigger(t, l, history)
	// Second trigger: 5 more ticks.
	runUntilTrigger(t, l, history)

	// Read the audit log.
	f, err := os.Open(l.privacy.AuditLogPath)
	if err != nil {
		t.Fatalf("open audit log %q: %v", l.privacy.AuditLogPath, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan audit log: %v", err)
	}

	if got, want := len(lines), 2; got != want {
		t.Fatalf("audit log line count = %d, want %d", got, want)
	}

	for i, line := range lines {
		var ent AuditEntry
		if err := json.Unmarshal([]byte(line), &ent); err != nil {
			t.Errorf("line %d not valid JSON: %v", i, err)
			continue
		}
		if ent.Category != "fact" {
			t.Errorf("line %d: Category = %q, want %q", i, ent.Category, "fact")
		}
		if ent.Content != "deploy target is gcp/us-central1" {
			t.Errorf("line %d: Content = %q, want the fact", i, ent.Content)
		}
		if ent.Decision != "auto-applied" {
			t.Errorf("line %d: Decision = %q, want %q", i, ent.Decision, "auto-applied")
		}
		if ent.Timestamp == "" {
			t.Errorf("line %d: Timestamp is empty", i)
		}
		if ent.ContentSHA == "" {
			t.Errorf("line %d: ContentSHA is empty", i)
		}
	}
}

// containsTag reports whether tags contains tag. Linear scan; tag lists
// are bounded to a handful of entries.
func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Compile-time: assert the curator constructor still implements the
// cortex.Lobe interface (sanity for the Run signature change in
// TASK-30; defensive against future refactors).
var _ cortex.Lobe = (*MemoryCuratorLobe)(nil)

// _ = llm.MetaActionKind keeps the llm import live in this _test.go
// file even when none of the tests exercise it directly. Removing
// this would break tests that read meta keys from confirm-Notes via
// the llm.MetaActionKind constant.
var _ = llm.MetaActionKind
