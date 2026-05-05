package memoryrecall

import (
	"context"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/wisdom"
)

// userTurn builds a single-message history slice with one user-role
// agentloop.Message carrying text. Centralized so tests stay readable.
func userTurn(text string) []agentloop.Message {
	return []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: text}}},
	}
}

// fakeMemoryStore is the in-memory test double. Real *memory.Store would
// also work but pulling in the on-disk JSON path complicates the test
// surface. The Lobe accepts the narrow memoryStore interface, so this
// struct satisfies it directly.
type fakeMemoryStore struct {
	entries []memory.Entry
}

func (f *fakeMemoryStore) Recall(query string, limit int) []memory.Entry {
	if limit <= 0 || len(f.entries) == 0 {
		return nil
	}
	out := make([]memory.Entry, 0, len(f.entries))
	// Tokenize the query on whitespace; an entry matches if ANY token is
	// present (substring) in Content or Context. This mimics the real
	// memory.Store.Recall behaviour (token-level scoring) closely enough
	// for unit tests without pulling in the full TF-IDF path.
	for _, e := range f.entries {
		if query == "" {
			out = append(out, e)
			if len(out) == limit {
				break
			}
			continue
		}
		matched := false
		for _, tok := range fields(query) {
			if containsAny(e.Content, tok) || containsAny(e.Context, tok) {
				matched = true
				break
			}
		}
		if matched {
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

// fields splits s on whitespace; standalone helper so the test file
// stays free of a strings import noise. Returns at most 16 tokens.
func fields(s string) []string {
	out := make([]string, 0, 4)
	cur := make([]byte, 0, 16)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' {
			if len(cur) > 0 {
				out = append(out, string(cur))
				cur = cur[:0]
			}
			continue
		}
		cur = append(cur, c)
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	if len(out) > 16 {
		out = out[:16]
	}
	return out
}

func (f *fakeMemoryStore) RecallByCategory(cat memory.Category) []memory.Entry {
	out := make([]memory.Entry, 0)
	for _, e := range f.entries {
		if e.Category == cat {
			out = append(out, e)
		}
	}
	return out
}

func containsAny(haystack, needle string) bool {
	if haystack == "" || needle == "" {
		return false
	}
	// Case-sensitive contains is enough for tests.
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

type fakeWisdomStore struct {
	items []wisdom.Learning
}

func (f *fakeWisdomStore) Learnings() []wisdom.Learning {
	out := make([]wisdom.Learning, len(f.items))
	copy(out, f.items)
	return out
}

// TestMemoryRecallLobe_BuildsIndexOnStart asserts that the first Run
// triggers a corpus rebuild and that DocCount reflects the union of
// memory + wisdom inputs. Spec item 6.
func TestMemoryRecallLobe_BuildsIndexOnStart(t *testing.T) {
	mem := &fakeMemoryStore{
		entries: []memory.Entry{
			{ID: "m1", Category: memory.CatGotcha, Content: "deadlock on close"},
			{ID: "m2", Category: memory.CatPattern, Content: "use defer cancel"},
			{ID: "m3", Category: memory.CatFix, Content: "added context cancellation"},
		},
	}
	wis := &fakeWisdomStore{
		items: []wisdom.Learning{
			{TaskID: "t1", Description: "build flake on race", Category: wisdom.Gotcha},
			{TaskID: "t2", Description: "prefer table-driven tests", Category: wisdom.Pattern},
		},
	}

	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	lobe := newMemoryRecallLobeForTest(ws, mem, wis, bus)

	if lobe.IndexBuilt() {
		t.Fatal("expected indexBuilt=false before Run")
	}

	if err := lobe.Run(context.Background(), cortex.LobeInput{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !lobe.IndexBuilt() {
		t.Fatal("expected indexBuilt=true after Run")
	}

	want := 5 // 3 memory + 2 wisdom
	if got := lobe.DocCount(); got != want {
		t.Fatalf("DocCount = %d, want %d", got, want)
	}
}

// TestMemoryRecallLobe_PublishesTopThreeNotes asserts that for a query
// matching multiple entries, exactly 3 SevInfo Notes land in the
// Workspace (the spec cap). Spec item 7.
func TestMemoryRecallLobe_PublishesTopThreeNotes(t *testing.T) {
	mem := &fakeMemoryStore{
		entries: []memory.Entry{
			{ID: "m1", Category: memory.CatGotcha, Content: "deadlock on close goroutine"},
			{ID: "m2", Category: memory.CatGotcha, Content: "deadlock when channel unbuffered"},
			{ID: "m3", Category: memory.CatPattern, Content: "deadlock avoided via select default"},
			{ID: "m4", Category: memory.CatFix, Content: "deadlock fix: drain channel before close"},
			{ID: "m5", Category: memory.CatFact, Content: "deadlock detection requires runtime trace"},
		},
	}
	wis := &fakeWisdomStore{}
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	lobe := newMemoryRecallLobeForTest(ws, mem, wis, bus)

	in := cortex.LobeInput{History: userTurn("how do I avoid a deadlock")}
	if err := lobe.Run(context.Background(), in); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := ws.Snapshot()
	// Filter out anything not from this lobe (defensive — should all be
	// memory-recall in this test).
	mine := make([]cortex.Note, 0, len(got))
	for _, n := range got {
		if n.LobeID == "memory-recall" {
			mine = append(mine, n)
		}
	}
	if len(mine) != 3 {
		t.Fatalf("published %d Notes, want 3 (got titles: %v)", len(mine), titlesOf(mine))
	}
	for _, n := range mine {
		if n.Severity != cortex.SevInfo {
			t.Errorf("note %s: severity=%v, want SevInfo", n.ID, n.Severity)
		}
	}
}

// TestMemoryRecallLobe_DedupesAcrossSources asserts that when the same
// content lands in BOTH memory and wisdom, only one Note is published.
// Spec item 7 (dedup clause).
func TestMemoryRecallLobe_DedupesAcrossSources(t *testing.T) {
	shared := "always close the response body"
	mem := &fakeMemoryStore{
		entries: []memory.Entry{
			{ID: "m1", Category: memory.CatGotcha, Content: shared},
		},
	}
	wis := &fakeWisdomStore{
		items: []wisdom.Learning{
			{TaskID: "t1", Description: shared, Category: wisdom.Gotcha},
		},
	}

	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	lobe := newMemoryRecallLobeForTest(ws, mem, wis, bus)

	in := cortex.LobeInput{History: userTurn("close the response body")}
	if err := lobe.Run(context.Background(), in); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mine := mineNotes(ws.Snapshot())
	if len(mine) != 1 {
		t.Fatalf("dedup failed: published %d Notes, want 1 (titles: %v)", len(mine), titlesOf(mine))
	}
}

// TestMemoryRecallLobe_ReindexesOnMemoryAdded asserts that publishing a
// hub.EventCortexWorkspaceMemoryAdded event after the lobe has built
// its index causes the next Run to surface a freshly added entry.
//
// The subscriber registered in NewMemoryRecallLobe rebuilds the index
// from the underlying store, which the test mutates between events.
//
// Spec item 8.
func TestMemoryRecallLobe_ReindexesOnMemoryAdded(t *testing.T) {
	mem := &fakeMemoryStore{
		entries: []memory.Entry{
			{ID: "m1", Category: memory.CatGotcha, Content: "initial deadlock entry"},
		},
	}
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	lobe := newMemoryRecallLobeForTest(ws, mem, &fakeWisdomStore{}, bus)

	// First Run builds the initial index (size 1).
	if err := lobe.Run(context.Background(), cortex.LobeInput{History: userTurn("anything")}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if got := lobe.DocCount(); got != 1 {
		t.Fatalf("initial DocCount = %d, want 1", got)
	}

	// Mutate the store and publish the reindex event. The handler runs
	// in observe mode — bus.Emit dispatches it inline (the runtime
	// invokes observers in goroutines but bus.Emit waits inside Phase 3
	// only via spawn). Use Emit synchronously so we can deterministically
	// observe the rebuild.
	mem.entries = append(mem.entries, memory.Entry{
		ID: "m2", Category: memory.CatPattern, Content: "added after first Run",
	})

	// Observe-mode handlers fire async. Use a sync gate by directly
	// invoking the subscriber: bus.Emit returns once gate+transform are
	// done but observers run in goroutines. Replace with a deterministic
	// rebuild trigger: emit and wait on DocCount.
	bus.EmitAsync(&hub.Event{Type: hub.EventCortexWorkspaceMemoryAdded})

	// Poll up to 2s for the observer to land. Production code does not
	// block on this — TASK-8 only requires "next Run uses the new entry".
	deadline := time.Now().Add(2 * time.Second)
	for lobe.DocCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if got := lobe.DocCount(); got != 2 {
		t.Fatalf("after memory_added DocCount = %d, want 2", got)
	}
}

// TestMemoryRecallLobe_NoOpWithoutUserMessage asserts that a Round with
// no user-role messages publishes nothing. Defensive: the trigger must
// never fire on assistant-only history (e.g. tool results).
func TestMemoryRecallLobe_NoOpWithoutUserMessage(t *testing.T) {
	mem := &fakeMemoryStore{
		entries: []memory.Entry{
			{ID: "m1", Category: memory.CatGotcha, Content: "x"},
		},
	}
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	lobe := newMemoryRecallLobeForTest(ws, mem, &fakeWisdomStore{}, bus)

	in := cortex.LobeInput{
		History: []agentloop.Message{
			{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: "x"}}},
		},
	}
	if err := lobe.Run(context.Background(), in); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(mineNotes(ws.Snapshot())); got != 0 {
		t.Fatalf("expected 0 Notes, got %d", got)
	}
}

// titlesOf is a tiny diagnostic helper for assertion failures.
func titlesOf(ns []cortex.Note) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Title
	}
	return out
}

// mineNotes filters a Workspace.Snapshot to only Notes from this lobe.
func mineNotes(all []cortex.Note) []cortex.Note {
	out := make([]cortex.Note, 0, len(all))
	for _, n := range all {
		if n.LobeID == "memory-recall" {
			out = append(out, n)
		}
	}
	return out
}

// TestMemoryRecallLobe_RedactsPrivateEntries asserts that an entry
// tagged "private" produces a Note whose Body is the redacted pointer
// line, NOT the entry content. Spec item 9.
func TestMemoryRecallLobe_RedactsPrivateEntries(t *testing.T) {
	mem := &fakeMemoryStore{
		entries: []memory.Entry{
			{
				ID:       "p1",
				Category: memory.CatPreference,
				Content:  "PASSWORD=hunter2 — never share with anyone",
				File:     "secrets/notes.md",
				Tags:     []string{"private"},
			},
		},
	}

	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	lobe := newMemoryRecallLobeForTest(ws, mem, &fakeWisdomStore{}, bus)

	if err := lobe.Run(context.Background(), cortex.LobeInput{History: userTurn("password share")}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mine := mineNotes(ws.Snapshot())
	if len(mine) != 1 {
		t.Fatalf("got %d Notes, want 1", len(mine))
	}
	n := mine[0]

	// The body MUST NOT contain the secret content.
	if containsAny(n.Body, "hunter2") || containsAny(n.Body, "PASSWORD") {
		t.Fatalf("private content leaked into Note.Body: %q", n.Body)
	}

	wantBody := "private memory exists about: secrets/notes.md"
	if n.Body != wantBody {
		t.Fatalf("Body = %q, want %q", n.Body, wantBody)
	}

	// The "private" tag must be preserved so downstream consumers can
	// filter the Note out of UI surfaces if they choose.
	hasPriv := false
	for _, tag := range n.Tags {
		if tag == "private" {
			hasPriv = true
		}
	}
	if !hasPriv {
		t.Errorf("expected 'private' tag preserved on Note, got tags=%v", n.Tags)
	}
}

// TestMemoryRecallLobe_RedactionFallsBackToBodyHint asserts the
// fallback behaviour when File is empty: the first 30 runes of body
// are used as the context hint. Spec item 9.
func TestMemoryRecallLobe_RedactionFallsBackToBodyHint(t *testing.T) {
	mem := &fakeMemoryStore{
		entries: []memory.Entry{
			{
				ID:       "p2",
				Category: memory.CatPreference,
				Content:  "abcdefghijklmnopqrstuvwxyz0123456789",
				Tags:     []string{"private"},
				// File intentionally empty
			},
		},
	}

	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	lobe := newMemoryRecallLobeForTest(ws, mem, &fakeWisdomStore{}, bus)

	if err := lobe.Run(context.Background(), cortex.LobeInput{History: userTurn("abcdefghij")}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mine := mineNotes(ws.Snapshot())
	if len(mine) != 1 {
		t.Fatalf("got %d Notes, want 1", len(mine))
	}

	wantPrefix := "private memory exists about: "
	if !startsWith(mine[0].Body, wantPrefix) {
		t.Fatalf("Body = %q, want prefix %q", mine[0].Body, wantPrefix)
	}
	// Hint must be exactly 30 runes (the spec cap).
	hint := mine[0].Body[len(wantPrefix):]
	runeCount := 0
	for range hint {
		runeCount++
	}
	if runeCount != 30 {
		t.Errorf("hint runes = %d, want 30 (hint=%q)", runeCount, hint)
	}
}

func startsWith(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}

// TestMemoryRecallLobe_NonPrivateEntriesAreNotRedacted asserts the
// negative case: entries WITHOUT the "private" tag pass through
// unredacted (Body == Content).
func TestMemoryRecallLobe_NonPrivateEntriesAreNotRedacted(t *testing.T) {
	mem := &fakeMemoryStore{
		entries: []memory.Entry{
			{ID: "n1", Category: memory.CatPattern, Content: "use defer cancel for ctx"},
		},
	}
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	lobe := newMemoryRecallLobeForTest(ws, mem, &fakeWisdomStore{}, bus)

	if err := lobe.Run(context.Background(), cortex.LobeInput{History: userTurn("defer cancel ctx")}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mine := mineNotes(ws.Snapshot())
	if len(mine) != 1 {
		t.Fatalf("got %d Notes, want 1", len(mine))
	}
	if !containsAny(mine[0].Body, "defer cancel") {
		t.Fatalf("non-private body should preserve content, got %q", mine[0].Body)
	}
}

// TestMemoryRecallLobe_LobeInterfaceShape pins the cortex.Lobe contract
// at compile-time. If the interface drifts, this fails to build.
func TestMemoryRecallLobe_LobeInterfaceShape(t *testing.T) {
	var _ cortex.Lobe = (*MemoryRecallLobe)(nil)

	lobe := newMemoryRecallLobeForTest(nil, &fakeMemoryStore{}, &fakeWisdomStore{}, nil)
	if got := lobe.ID(); got != "memory-recall" {
		t.Errorf("ID = %q, want memory-recall", got)
	}
	if lobe.Kind() != cortex.KindDeterministic {
		t.Errorf("Kind = %v, want KindDeterministic", lobe.Kind())
	}
	if lobe.Description() == "" {
		t.Error("Description must be non-empty")
	}
}
