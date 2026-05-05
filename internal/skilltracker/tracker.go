// Package skilltracker tracks which skills are currently loaded
// into each stance's concern field, and emits SkillUnloaded ledger
// nodes when a skill leaves the active set.
//
// Issues #157 (compactor caller) and #158 (scope-exit caller) both
// need this layer. Spec 3 (PR #156) shipped EmitSkillUnloaded as a
// reusable helper and noted both callers were BLOCKED-PARTIAL
// because internal/microcompact/compact.go works on label-keyed
// text Sections (no per-skill granularity) and
// internal/agentloop/scope.go didn't exist. Tracker is the layer
// that bridges the two — the compactor + scope manager don't need
// to know about skills directly; they call Tracker.Drop /
// Tracker.CloseScope and Tracker handles emission.
//
// State model:
//
//   loaded[stanceID][skillRef] = LoadInfo{loadID, taskScope, ...}
//
// LoadID is the ledger NodeID of the originating SkillLoaded entry.
// It threads back into nodes.SkillUnloaded.LoadRef so the v2
// dashboard can pair load + unload events in the side panel.
//
// Concurrency: every method takes the tracker mutex. Skill loads
// and unloads in r1 happen at compactor / phase-exit boundaries —
// not in tight loops — so the lock is uncontended in practice.
package skilltracker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/hub/builtin"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
)

// LoadInfo holds the per-load metadata Tracker needs to reconstruct
// a nodes.SkillUnloaded when the skill leaves the active set.
type LoadInfo struct {
	LoadID     ledger.NodeID // SkillLoaded ledger node id
	StanceID   string
	StanceRole string
	SkillRef   string
	TaskScope  string
	LoadedAt   time.Time
	Tokens     int // BudgetTokensFreed populated on compactor_evicted unloads
}

// Tracker is the in-memory load-state table. The compactor + scope
// manager call Drop / CloseScope; the SkillInjector calls Note on
// every successful load. Tracker owns the ledger emission so
// callers don't have to thread *ledger.Ledger through their hot
// paths.
type Tracker struct {
	mu      sync.Mutex
	led     *ledger.Ledger
	loaded  map[string]map[string]LoadInfo // stanceID → skillRef → LoadInfo
}

// New constructs a Tracker bound to the given ledger. A nil ledger
// makes every emission a no-op (returns nil) — matches
// EmitSkillUnloaded's best-effort contract so feature-flagged
// callers can keep the same call site whether emission is wired in.
func New(led *ledger.Ledger) *Tracker {
	return &Tracker{
		led:    led,
		loaded: map[string]map[string]LoadInfo{},
	}
}

// Note records that a skill is now loaded in a stance. Idempotent:
// calling Note twice for the same (stanceID, skillRef) overwrites
// the prior LoadInfo (typical when a skill is re-injected after
// a compaction round). Spec 3 §5.1's emission path should call
// Note immediately after the SkillInjector writes the SkillLoaded
// node so the LoadID is fresh.
func (t *Tracker) Note(info LoadInfo) error {
	if info.StanceID == "" {
		return errors.New("skilltracker: stance_id is required")
	}
	if info.SkillRef == "" {
		return errors.New("skilltracker: skill_ref is required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	bucket := t.loaded[info.StanceID]
	if bucket == nil {
		bucket = map[string]LoadInfo{}
		t.loaded[info.StanceID] = bucket
	}
	bucket[info.SkillRef] = info
	return nil
}

// Drop emits a SkillUnloaded for one (stanceID, skillRef) and
// removes it from the tracker. Reason MUST be one of the values
// nodes.SkillUnloaded.Validate accepts. Returns the assigned NodeID
// from the ledger (or empty string when ledger is nil / skill
// wasn't tracked / emission failed).
//
// Drop is idempotent: calling it on a (stance, skill) pair that
// isn't loaded returns "" + nil rather than erroring. Lets the
// compactor sweep through "evict skills X,Y,Z" without first
// checking which are still loaded.
func (t *Tracker) Drop(ctx context.Context, stanceID, skillRef, reason string) (ledger.NodeID, error) {
	t.mu.Lock()
	bucket := t.loaded[stanceID]
	info, ok := bucket[skillRef]
	if ok {
		delete(bucket, skillRef)
		if len(bucket) == 0 {
			delete(t.loaded, stanceID)
		}
	}
	t.mu.Unlock()
	if !ok {
		return "", nil
	}
	if t.led == nil {
		return "", nil
	}
	n := &nodes.SkillUnloaded{
		SkillRef:          info.SkillRef,
		LoadRef:           string(info.LoadID),
		StanceID:          info.StanceID,
		StanceRole:        info.StanceRole,
		Reason:            reason,
		BudgetTokensFreed: info.Tokens,
		CreatedAt:         time.Now().UTC(),
		Version:           1,
	}
	id, err := builtin.EmitSkillUnloaded(ctx, t.led, n)
	if err != nil {
		return "", fmt.Errorf("skilltracker drop %s/%s: %w", stanceID, skillRef, err)
	}
	return id, nil
}

// CloseScope drops every skill loaded into the given (stanceID,
// taskScope) pair, emitting one SkillUnloaded per dropped skill
// with Reason="scope_exit". Used by the agent-loop scope-manager
// when a task DAG closes. Returns the count of skills dropped + the
// first emission error (subsequent emissions still fire so the
// audit trail is complete; the first error is returned for caller
// visibility).
//
// Idempotent: closing an empty scope returns (0, nil).
func (t *Tracker) CloseScope(ctx context.Context, stanceID, taskScope string) (int, error) {
	t.mu.Lock()
	bucket := t.loaded[stanceID]
	if bucket == nil {
		t.mu.Unlock()
		return 0, nil
	}
	// Snapshot under lock; emit outside.
	toEmit := make([]LoadInfo, 0, len(bucket))
	for skillRef, info := range bucket {
		if info.TaskScope == taskScope {
			toEmit = append(toEmit, info)
			delete(bucket, skillRef)
		}
	}
	if len(bucket) == 0 {
		delete(t.loaded, stanceID)
	}
	t.mu.Unlock()
	if len(toEmit) == 0 {
		return 0, nil
	}
	if t.led == nil {
		return len(toEmit), nil
	}
	var firstErr error
	now := time.Now().UTC()
	for _, info := range toEmit {
		n := &nodes.SkillUnloaded{
			SkillRef:   info.SkillRef,
			LoadRef:    string(info.LoadID),
			StanceID:   info.StanceID,
			StanceRole: info.StanceRole,
			Reason:     "scope_exit",
			CreatedAt:  now,
			Version:    1,
		}
		if _, err := builtin.EmitSkillUnloaded(ctx, t.led, n); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("skilltracker close-scope %s/%s: %w", stanceID, info.SkillRef, err)
		}
	}
	return len(toEmit), firstErr
}

// EvictByCompactor is the issue #157 caller entry. Drops every
// skill in the supplied list with Reason="compactor_evicted" and
// records BudgetTokensFreed per skill via the optional callback
// (the compactor knows the token cost of each skill block; we don't
// require it but accept it).
//
// Returns the count of skills dropped + the first emission error.
func (t *Tracker) EvictByCompactor(ctx context.Context, stanceID string, evicted []EvictionRequest) (int, error) {
	if len(evicted) == 0 {
		return 0, nil
	}
	t.mu.Lock()
	bucket := t.loaded[stanceID]
	if bucket == nil {
		t.mu.Unlock()
		return 0, nil
	}
	type evictPair struct {
		info   LoadInfo
		tokens int
	}
	toEmit := make([]evictPair, 0, len(evicted))
	for _, e := range evicted {
		if info, ok := bucket[e.SkillRef]; ok {
			info.Tokens = e.TokensFreed
			toEmit = append(toEmit, evictPair{info: info, tokens: e.TokensFreed})
			delete(bucket, e.SkillRef)
		}
	}
	if len(bucket) == 0 {
		delete(t.loaded, stanceID)
	}
	t.mu.Unlock()
	if len(toEmit) == 0 {
		return 0, nil
	}
	if t.led == nil {
		return len(toEmit), nil
	}
	var firstErr error
	now := time.Now().UTC()
	for _, p := range toEmit {
		n := &nodes.SkillUnloaded{
			SkillRef:          p.info.SkillRef,
			LoadRef:           string(p.info.LoadID),
			StanceID:          p.info.StanceID,
			StanceRole:        p.info.StanceRole,
			Reason:            "compactor_evicted",
			BudgetTokensFreed: p.tokens,
			CreatedAt:         now,
			Version:           1,
		}
		if _, err := builtin.EmitSkillUnloaded(ctx, t.led, n); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("skilltracker compactor-evict %s/%s: %w", stanceID, p.info.SkillRef, err)
		}
	}
	return len(toEmit), firstErr
}

// EvictionRequest is one entry in EvictByCompactor's input.
type EvictionRequest struct {
	SkillRef    string
	TokensFreed int
}

// Loaded reports whether (stanceID, skillRef) is currently in the
// tracker. Useful for tests + idempotency checks in the caller.
func (t *Tracker) Loaded(stanceID, skillRef string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	bucket := t.loaded[stanceID]
	if bucket == nil {
		return false
	}
	_, ok := bucket[skillRef]
	return ok
}

// Snapshot returns a copy of the loaded state. Useful for tests
// and the v2 dashboard's "currently loaded skills" panel (a future
// feature).
func (t *Tracker) Snapshot() map[string]map[string]LoadInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]map[string]LoadInfo, len(t.loaded))
	for stanceID, bucket := range t.loaded {
		copyB := make(map[string]LoadInfo, len(bucket))
		for k, v := range bucket {
			copyB[k] = v
		}
		out[stanceID] = copyB
	}
	return out
}
