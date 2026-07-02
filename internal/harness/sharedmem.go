package harness

import (
	"context"
	"fmt"
	"time"

	"github.com/RelayOne/r1/internal/sharedmem"
)

// sharedmem.go activates STOKE-017 (internal/sharedmem) for the harness:
// concurrent stance workers collaborate on reducer-mediated shared-memory
// blocks, each write carrying PROV-AGENT provenance stamped with the writing
// stance's ID. Before this seam the sharedmem package had zero importers —
// a complete, tested deliverable no agent could reach (audit A070). The
// harness now owns one NamespacedStore per mission and binds each stance to
// its collaboration namespace via StanceMemory, so two stances in the same
// consensus loop (or task, or mission) share blocks while stances outside
// that namespace are denied (sharedmem.ErrNamespaceDenied).

// RegisterSharedReducer installs a reducer on the harness shared-memory store
// for blockType. Stances that Insert into a block of that type get their
// concurrent writes merged by the reducer (e.g. sharedmem.AddReducer for an
// append-only findings log, sharedmem.UnionReducer for a tag set). Reducers
// must be registered before the first Insert to a block of the matching type.
func (h *Harness) RegisterSharedReducer(blockType sharedmem.BlockType, r sharedmem.Reducer) {
	h.memInner.RegisterReducer(blockType, r)
}

// SharedMemory returns the mission-wide namespace-scoped store. Prefer
// StanceMemory for stance-authored writes so provenance and the namespace
// allow-list are bound automatically; this accessor exists for callers that
// need the raw store (tests, cross-cutting inspectors).
func (h *Harness) SharedMemory() *sharedmem.NamespacedStore { return h.mem }

// stanceNamespace returns the sharedmem namespace a stance collaborates in.
// Stances in the same consensus loop share one namespace; absent a loop they
// fall back to task scope, then mission scope, then the global default. This
// is what makes two proposing/reviewing stances on one loop see each other's
// reducer-mediated blocks while an unrelated task's stances stay isolated.
func (h *Harness) stanceNamespace(sess *StanceSession) string {
	switch {
	case sess.SpawnRequest.LoopRef != "":
		return "loop:" + sess.SpawnRequest.LoopRef
	case sess.SpawnRequest.TaskDAGScope != "":
		return "task:" + sess.SpawnRequest.TaskDAGScope
	case h.config.MissionID != "":
		return "mission:" + h.config.MissionID
	default:
		return "default"
	}
}

// StanceMemory returns a per-stance view of the harness shared-memory store,
// bound to the stance's identity and collaboration namespace. Every read is
// namespace-checked and every write carries PROV-AGENT provenance stamped
// with the stance's ID. Returns an error if the stance is unknown.
func (h *Harness) StanceMemory(stanceID string) (*StanceMemory, error) {
	h.mu.RLock()
	sess, ok := h.stances[stanceID]
	h.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("harness: sharedmem: stance %q not found", stanceID)
	}
	ns := h.stanceNamespace(sess)
	return &StanceMemory{
		store:    h.mem,
		callerID: sess.ID,
		agentID:  sess.ID,
		ns:       ns,
		allow:    sharedmem.NewAllowList(ns),
	}, nil
}

// StanceMemory is a per-stance handle onto the harness shared-memory store.
// It binds a stance's identity (callerID/agentID) and namespace allow-list so
// every read is namespace-checked and every write carries PROV-AGENT
// provenance stamped with the stance's ID. Stances that resolve to the same
// namespace (same loop/task/mission) share the same blocks; the reducer
// registered for a block's type resolves their concurrent Inserts.
type StanceMemory struct {
	store    *sharedmem.NamespacedStore
	callerID string
	agentID  string
	ns       string
	allow    sharedmem.NamespaceAllowList
}

// Namespace reports the shared namespace this stance collaborates in.
func (m *StanceMemory) Namespace() string { return m.ns }

// CreateBlock creates a new shared block in the stance's namespace with an
// initial value. The block's namespace is forced to the stance's namespace so
// a stance can only seed blocks other collaborators in that namespace can see.
// The creation provenance entry is stamped with the stance's ID.
func (m *StanceMemory) CreateBlock(ctx context.Context, id sharedmem.BlockID, blockType sharedmem.BlockType, label string, value any) (*sharedmem.Block, error) {
	block := &sharedmem.Block{
		ID:         id,
		Type:       blockType,
		Label:      label,
		Namespace:  m.ns,
		Value:      value,
		Provenance: []sharedmem.ProvenanceEntry{m.prov("create", "")},
	}
	if err := m.store.Create(ctx, block); err != nil {
		return nil, err
	}
	return m.store.Get(ctx, id, m.callerID, m.allow)
}

// Insert applies an additive, reducer-mediated write to a shared block.
// Concurrent Inserts from other stances in the namespace merge via the
// block-type's registered reducer. Fails with sharedmem.ErrNamespaceDenied if
// the block is outside this stance's namespace.
func (m *StanceMemory) Insert(ctx context.Context, id sharedmem.BlockID, value any, note string) (*sharedmem.Block, error) {
	return m.store.Apply(ctx, sharedmem.Write{
		BlockID:    id,
		Semantic:   sharedmem.SemanticInsert,
		Value:      value,
		Provenance: m.prov("insert", note),
	}, m.callerID, m.allow)
}

// Replace applies an optimistic-concurrency write: it succeeds only if the
// block's current version equals expectedVersion, otherwise returns
// sharedmem.ErrVersionMismatch and the caller re-reads and retries.
func (m *StanceMemory) Replace(ctx context.Context, id sharedmem.BlockID, value any, expectedVersion int, note string) (*sharedmem.Block, error) {
	return m.store.Apply(ctx, sharedmem.Write{
		BlockID:         id,
		Semantic:        sharedmem.SemanticReplace,
		Value:           value,
		ExpectedVersion: expectedVersion,
		Provenance:      m.prov("replace", note),
	}, m.callerID, m.allow)
}

// Rethink applies a last-writer-wins write, overwriting the block's value
// unconditionally (still recording provenance so history is preserved).
func (m *StanceMemory) Rethink(ctx context.Context, id sharedmem.BlockID, value any, note string) (*sharedmem.Block, error) {
	return m.store.Apply(ctx, sharedmem.Write{
		BlockID:    id,
		Semantic:   sharedmem.SemanticRethink,
		Value:      value,
		Provenance: m.prov("rethink", note),
	}, m.callerID, m.allow)
}

// Get reads a shared block, enforcing the stance's namespace allow-list.
func (m *StanceMemory) Get(ctx context.Context, id sharedmem.BlockID) (*sharedmem.Block, error) {
	return m.store.Get(ctx, id, m.callerID, m.allow)
}

// Subscribe returns a channel of future updates to the block, gated by the
// stance's namespace allow-list. The channel closes when ctx is done.
func (m *StanceMemory) Subscribe(ctx context.Context, id sharedmem.BlockID) (<-chan *sharedmem.Block, error) {
	return m.store.Subscribe(ctx, id, m.callerID, m.allow)
}

// prov builds a PROV-AGENT provenance entry stamped with the stance's ID and
// the current time — the minimum an auditable write requires
// (sharedmem.validateProvEntry).
func (m *StanceMemory) prov(action, note string) sharedmem.ProvenanceEntry {
	return sharedmem.ProvenanceEntry{
		AgentID:   m.agentID,
		Action:    action,
		Timestamp: time.Now().UTC(),
		Note:      note,
	}
}
