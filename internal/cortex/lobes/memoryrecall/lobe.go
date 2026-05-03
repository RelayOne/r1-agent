// Package memoryrecall implements the MemoryRecallLobe — a Deterministic
// Lobe that surfaces relevant memory.Store and wisdom.Store entries as
// Notes based on the last user message in each Round.
//
// Spec: specs/cortex-concerns.md items 6–9 ("MemoryRecallLobe").
//
// Design summary:
//
//   - Construction: NewMemoryRecallLobe(ws, mem, wis, bus). The Lobe holds
//     a writable *cortex.Workspace handle (LobeInput.Workspace is the
//     read-only adapter — Lobes that publish must capture the write
//     handle at construction time, mirroring the EchoLobe pattern in
//     internal/cortex/lobe_test.go:21).
//
//   - Index: a tfidf.Index built lazily on first Run. The corpus is the
//     union of (a) every memory.Entry across all six Categories and (b)
//     every wisdom.Learning. Documents are keyed by a synthetic ID so
//     dedup across sources works on identical Body text.
//
//   - Per-Round trigger: cortex-core's LobeRunner Tick fires Run once per
//     Round. Run extracts the last user message (last 1000 chars) from
//     LobeInput.History, queries memory.Store.Recall + tfidf.Index.Search,
//     dedups by Body, and Publishes the top-3 hits as SevInfo Notes.
//
//   - Reindex: on construction, the Lobe registers a hub.Subscriber on
//     EventCortexWorkspaceMemoryAdded. Each event triggers a full rebuild
//     (the corpus is small enough that incremental indexing is not worth
//     the complexity, per spec item 8).
//
//   - Privacy (item 9): when an indexed entry's Tags contain "private",
//     the published Note's Body is redacted to "private memory exists
//     about: <File or first 30 chars of Context/Content>" instead of the
//     entry content.
package memoryrecall

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/tfidf"
	"github.com/RelayOne/r1/internal/wisdom"
)

// memoryStore is the read-only subset of *memory.Store this Lobe needs.
// Defined as an interface so tests can inject a stub without spinning up
// the on-disk JSON store. *memory.Store satisfies this implicitly.
type memoryStore interface {
	Recall(query string, limit int) []memory.Entry
	RecallByCategory(cat memory.Category) []memory.Entry
}

// wisdomStore is the read-only subset of *wisdom.Store this Lobe needs.
type wisdomStore interface {
	Learnings() []wisdom.Learning
}

// allMemoryCategories enumerates the six well-known memory.Category
// values the Lobe sweeps for index construction. Kept as a package-level
// var so tests can introspect / extend.
var allMemoryCategories = []memory.Category{
	memory.CatGotcha,
	memory.CatPattern,
	memory.CatPreference,
	memory.CatFact,
	memory.CatAntiPattern,
	memory.CatFix,
}

// MemoryRecallLobe is the cortex.Lobe implementation declared in spec
// items 6–9. It is KindDeterministic — it makes no LLM calls.
type MemoryRecallLobe struct {
	ws  *cortex.Workspace
	mem memoryStore
	wis wisdomStore
	bus *hub.Bus

	mu         sync.Mutex
	idx        *tfidf.Index
	docs       []indexedDoc // parallel to idx; tfidf returns Path which keys back into this slice
	indexBuilt bool
}

// indexedDoc is the per-document metadata the Lobe carries alongside the
// tfidf.Index. tfidf only stores path+terms; the Lobe needs the full
// body, tags, and source-file hint to render Notes (and to apply the
// privacy redaction in TASK-9).
type indexedDoc struct {
	id      string   // synthetic key — also used as tfidf path
	body    string   // human-readable content, used as Note.Body
	tags    []string // entry tags (e.g. "private")
	file    string   // source file hint, used in privacy redaction
	source  string   // "memory" | "wisdom"
}

// NewMemoryRecallLobe constructs the Lobe. ws is the writable Workspace
// the Lobe Publishes into; mem and wis are the upstream stores the index
// pulls from; bus is the event hub used to subscribe for incremental
// reindex. bus may be nil in tests that drive Run directly.
//
// The constructor registers the memory_added subscriber synchronously so
// the first event after construction triggers a rebuild even if Run has
// not yet been called.
func NewMemoryRecallLobe(ws *cortex.Workspace, mem *memory.Store, wis *wisdom.Store, bus *hub.Bus) *MemoryRecallLobe {
	l := &MemoryRecallLobe{
		ws:  ws,
		mem: mem,
		wis: wis,
		bus: bus,
	}
	l.registerReindexSubscriber()
	return l
}

// newMemoryRecallLobeForTest is the test-facing constructor that accepts
// the narrow interfaces directly. Production code uses NewMemoryRecallLobe.
func newMemoryRecallLobeForTest(ws *cortex.Workspace, mem memoryStore, wis wisdomStore, bus *hub.Bus) *MemoryRecallLobe {
	l := &MemoryRecallLobe{
		ws:  ws,
		mem: mem,
		wis: wis,
		bus: bus,
	}
	l.registerReindexSubscriber()
	return l
}

// ID satisfies cortex.Lobe. Stable string used as Note.LobeID.
func (l *MemoryRecallLobe) ID() string { return "memory-recall" }

// Description satisfies cortex.Lobe. Used by /status output.
func (l *MemoryRecallLobe) Description() string {
	return "surfaces relevant memory + wisdom entries as Notes per Round"
}

// Kind satisfies cortex.Lobe. Deterministic — no LLM calls, no semaphore.
func (l *MemoryRecallLobe) Kind() cortex.LobeKind { return cortex.KindDeterministic }

// Run is the per-Round entry point. Spec items 6–7:
//
//  1. Lazily build the tfidf index on first call.
//  2. Extract the last user message from in.History (last 1000 chars).
//  3. Query both mem.Recall(query, 5) AND idx.Search(query, 5).
//  4. Dedup hits across the two sources by Body text.
//  5. Publish the top-3 deduped hits as SevInfo Notes.
//
// A nil Workspace is treated as a no-op publish target (test harnesses
// that exercise indexing without Publish use this path).
func (l *MemoryRecallLobe) Run(ctx context.Context, in cortex.LobeInput) error {
	if err := ctx.Err(); err != nil {
		return nil
	}

	l.mu.Lock()
	if !l.indexBuilt {
		l.rebuildIndexLocked()
	}
	l.mu.Unlock()

	query := lastUserMessage(in.History)
	if query == "" {
		return nil
	}

	hits := l.searchAndDedup(query, 3)
	if l.ws == nil {
		return nil
	}
	for _, doc := range hits {
		note := l.docToNote(doc)
		if err := l.ws.Publish(note); err != nil {
			// Publish errors here are not fatal — spec contract only
			// requires "Lobe MUST observe ctx.Done() and return nil".
			// A failed Publish (validation or persistence) should not
			// crash the runner; log via the bus and continue.
			continue
		}
	}
	return nil
}

// IndexBuilt reports whether the corpus has been indexed at least once.
// Test-facing accessor; production callers use Run.
func (l *MemoryRecallLobe) IndexBuilt() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.indexBuilt
}

// DocCount returns the number of indexed documents. Test-facing.
func (l *MemoryRecallLobe) DocCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.idx == nil {
		return 0
	}
	return l.idx.DocCount()
}

// rebuildIndexLocked rebuilds l.idx + l.docs from scratch. Caller MUST
// hold l.mu. Each call replaces both fields wholesale; no incremental
// state survives. Spec item 8 explicitly authorizes "rebuild from
// scratch each time" because the corpus is small.
func (l *MemoryRecallLobe) rebuildIndexLocked() {
	docs := make([]indexedDoc, 0)
	seen := make(map[string]bool) // dedup memory IDs across categories

	if l.mem != nil {
		for _, cat := range allMemoryCategories {
			for _, e := range l.mem.RecallByCategory(cat) {
				if seen[e.ID] {
					continue
				}
				seen[e.ID] = true
				docs = append(docs, entryToDoc(e))
			}
		}
	}

	if l.wis != nil {
		for i, lr := range l.wis.Learnings() {
			docs = append(docs, learningToDoc(i, lr))
		}
	}

	idx := tfidf.NewIndex()
	for _, d := range docs {
		idx.AddDocument(d.id, d.body)
	}
	idx.Finalize()

	l.idx = idx
	l.docs = docs
	l.indexBuilt = true
}

// entryToDoc projects a memory.Entry into the indexedDoc shape. Body is
// the searchable text (Content + Context joined). File is preserved for
// the privacy redaction path (TASK-9). Tags are copied so the lobe can
// detect the "private" tag.
func entryToDoc(e memory.Entry) indexedDoc {
	body := e.Content
	if e.Context != "" {
		body = body + "\n" + e.Context
	}
	tags := make([]string, len(e.Tags))
	copy(tags, e.Tags)
	return indexedDoc{
		id:     "mem:" + e.ID,
		body:   body,
		tags:   tags,
		file:   e.File,
		source: "memory",
	}
}

// learningToDoc projects a wisdom.Learning into the indexedDoc shape.
// wisdom has no Tags field, so privacy redaction never triggers for
// learnings. The synthetic id encodes index+TaskID for stable dedup.
func learningToDoc(i int, lr wisdom.Learning) indexedDoc {
	id := fmt.Sprintf("wis:%d:%s", i, lr.TaskID)
	return indexedDoc{
		id:     id,
		body:   lr.Description,
		tags:   nil,
		file:   lr.File,
		source: "wisdom",
	}
}

// searchAndDedup queries both data sources (memory.Recall + tfidf.Search)
// for query, dedups results across the two sources by Body text, and
// returns up to limit indexedDoc records ranked memory-first then by
// tfidf score. Acquires l.mu in read shape to read the index/docs slice
// (the underlying tfidf.Index is read-only after Finalize, so a single
// lock acquisition for the whole search is sufficient).
func (l *MemoryRecallLobe) searchAndDedup(query string, limit int) []indexedDoc {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.idx == nil {
		return nil
	}

	out := make([]indexedDoc, 0, limit)
	seenBody := make(map[string]bool)

	// Memory.Recall first — its scoring is content-aware (UseCount,
	// Confidence) so it tends to surface authoritative entries before
	// the unweighted tfidf signal does.
	if l.mem != nil {
		for _, e := range l.mem.Recall(query, 5) {
			doc := entryToDoc(e)
			if seenBody[doc.body] {
				continue
			}
			seenBody[doc.body] = true
			out = append(out, doc)
			if len(out) >= limit {
				return out
			}
		}
	}

	// tfidf fills any remaining slots. Result.Path is the synthetic id
	// we stamped during AddDocument; map back through l.docs.
	idByDoc := make(map[string]int, len(l.docs))
	for i, d := range l.docs {
		idByDoc[d.id] = i
	}
	for _, r := range l.idx.Search(query, 5) {
		idx, ok := idByDoc[r.Path]
		if !ok {
			continue
		}
		doc := l.docs[idx]
		if seenBody[doc.body] {
			continue
		}
		seenBody[doc.body] = true
		out = append(out, doc)
		if len(out) >= limit {
			return out
		}
	}
	return out
}

// docToNote renders an indexedDoc into a cortex.Note ready for Publish.
// Title is a short summary derived from Body (first 80 runes capped per
// Note.Validate). Body is the full doc body; tags carry the source plus
// the original entry tags so dashboards can filter ("source:memory" /
// "source:wisdom"). The privacy redaction (TASK-9) layers on top of
// this in a later commit.
func (l *MemoryRecallLobe) docToNote(d indexedDoc) cortex.Note {
	title := truncateRunes(firstLine(d.body), 80)
	if title == "" {
		title = "memory-recall hit"
	}
	tags := make([]string, 0, len(d.tags)+1)
	if d.source != "" {
		tags = append(tags, "source:"+d.source)
	}
	tags = append(tags, d.tags...)
	return cortex.Note{
		LobeID:   l.ID(),
		Severity: cortex.SevInfo,
		Title:    title,
		Body:     d.body,
		Tags:     tags,
	}
}

// lastUserMessage extracts the most recent user-role message text from
// the LobeInput.History slice and clips it to the last 1000 chars. If no
// user message is present, returns "" — the caller treats that as a
// no-op Round.
func lastUserMessage(history []agentloop.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Role != "user" {
			continue
		}
		text := extractText(m.Content)
		if text == "" {
			continue
		}
		if len(text) > 1000 {
			text = text[len(text)-1000:]
		}
		return text
	}
	return ""
}

// extractText collapses a slice of agentloop.ContentBlock into a single
// concatenated string of every "text"-typed block. Tool-result and
// tool-use blocks are skipped — they never carry a user-typed message
// in the cortex pipeline.
func extractText(blocks []agentloop.ContentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type != "" && blk.Type != "text" {
			continue
		}
		if blk.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(blk.Text)
	}
	return b.String()
}

// firstLine returns the substring up to the first newline, with leading
// and trailing whitespace trimmed. Empty input returns "".
func firstLine(s string) string {
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// truncateRunes returns at most n runes from s. The Note.Validate
// invariant is that Title <= 80 runes, so this is the canonical cap
// used at the publish boundary.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// registerReindexSubscriber wires the hub.Subscriber that triggers a
// full reindex on every cortex.workspace.memory_added event. Implements
// spec item 8. Safe with a nil bus (no-op).
func (l *MemoryRecallLobe) registerReindexSubscriber() {
	if l.bus == nil {
		return
	}
	l.bus.Register(hub.Subscriber{
		ID:     "memory-recall-reindex",
		Events: []hub.EventType{hub.EventCortexWorkspaceMemoryAdded},
		Mode:   hub.ModeObserve,
		Handler: func(ctx context.Context, ev *hub.Event) *hub.HookResponse {
			l.mu.Lock()
			l.rebuildIndexLocked()
			l.mu.Unlock()
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})
}
