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
	"sync"

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

// Run is the per-Round entry point. Spec items 6–7: build the index on
// first call (lazy), then publish the top-3 Notes for the latest user
// message. TASK-7 (publish + dedup) and TASK-9 (privacy redaction) layer
// on top of this scaffold and are added in subsequent commits.
func (l *MemoryRecallLobe) Run(ctx context.Context, in cortex.LobeInput) error {
	if err := ctx.Err(); err != nil {
		return nil
	}

	l.mu.Lock()
	if !l.indexBuilt {
		l.rebuildIndexLocked()
	}
	l.mu.Unlock()

	// Publish logic lands in TASK-7. Scaffold returns nil so the runner
	// observes a clean Run.
	_ = in
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
