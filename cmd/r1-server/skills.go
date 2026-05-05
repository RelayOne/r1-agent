// Package main — skills.go
//
// Spec 3 §4.2: per-request SkillEventMap that indexes skill_loaded +
// skill_unloaded ledger nodes by (stanceID, skillRef) so the
// waterfall, side panel, and 3D scrubber can answer "is skill X
// active in stance Y at time T?" in O(log N) per query.
//
// Like RedactionMap, this is loaded ONCE per request from the ledger
// and threaded through into every render that needs the active-state
// predicate. The 60-second TTL cache mirrors LoadRedactionMapCached
// for symmetry — same 1k-event session budget applies.
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
)

// SkillEvent is the per-load or per-unload datum the UI renders.
type SkillEvent struct {
	Type       string    `json:"type"`        // "skill_loaded" | "skill_unloaded"
	NodeID     string    `json:"node_id"`     // ledger node id of the event itself
	SkillRef   string    `json:"skill_ref"`
	StanceID   string    `json:"stance_id"`
	StanceRole string    `json:"stance_role"`
	LoadRef    string    `json:"load_ref,omitempty"` // SkillUnloaded.LoadRef
	Reason     string    `json:"reason,omitempty"`   // SkillUnloaded.Reason
	Tokens     int       `json:"tokens,omitempty"`   // SkillUnloaded.BudgetTokensFreed
	At         time.Time `json:"at"`
}

// SkillEventMap groups events by (stanceID, skillRef) — that pair
// uniquely identifies a load/unload lifecycle. Within each group,
// events are sorted by At ascending.
type SkillEventMap struct {
	byStanceSkill map[stanceSkillKey][]SkillEvent
}

type stanceSkillKey struct {
	stanceID string
	skillRef string
}

// IsActiveAt reports whether the skill is active in the given stance
// at time t. Active means: the most recent event ≤ t is a "skill_loaded"
// (no unload has occurred since).
func (m *SkillEventMap) IsActiveAt(stanceID, skillRef string, t time.Time) bool {
	if m == nil {
		return false
	}
	events := m.byStanceSkill[stanceSkillKey{stanceID, skillRef}]
	if len(events) == 0 {
		return false
	}
	// Binary search for the rightmost event ≤ t.
	// sort.Search returns smallest index i where events[i].At > t.
	i := sort.Search(len(events), func(i int) bool { return events[i].At.After(t) })
	if i == 0 {
		return false // No event ≤ t.
	}
	last := events[i-1]
	return last.Type == "skill_loaded"
}

// EventsForStance returns events for one (stance, skill) pair in
// chronological order. Empty slice if none.
func (m *SkillEventMap) EventsForStance(stanceID, skillRef string) []SkillEvent {
	if m == nil {
		return nil
	}
	return m.byStanceSkill[stanceSkillKey{stanceID, skillRef}]
}

// EventsBySkill returns every event for a skill across all stances,
// sorted chronologically. Used by the 3D graph layer to dim/restore
// a skill node.
func (m *SkillEventMap) EventsBySkill(skillRef string) []SkillEvent {
	if m == nil {
		return nil
	}
	out := []SkillEvent{}
	for k, events := range m.byStanceSkill {
		if k.skillRef != skillRef {
			continue
		}
		out = append(out, events...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// LoadSkillEventMap walks the chain-tier ledger once and groups every
// skill_loaded / skill_unloaded node by (stanceID, skillRef). O(N)
// in nodes scanned + O(K log K) for the per-key sort.
//
// Callers that hold a sessionID may filter further; the current
// ledger surface doesn't expose per-session listing, so an empty
// sessionID is treated as "all events". Spec 3 §10 acknowledges
// this and lists per-session filtering as future work.
func LoadSkillEventMap(store *ledger.Store, sessionID string) (*SkillEventMap, error) {
	if store == nil {
		return &SkillEventMap{byStanceSkill: map[stanceSkillKey][]SkillEvent{}}, nil
	}
	all, err := store.ListNodes()
	if err != nil {
		return nil, fmt.Errorf("skill event map: list nodes: %w", err)
	}
	out := &SkillEventMap{byStanceSkill: map[stanceSkillKey][]SkillEvent{}}
	for _, n := range all {
		switch n.Type {
		case "skill_loaded":
			ev, err := unpackSkillLoaded(n)
			if err != nil {
				continue
			}
			k := stanceSkillKey{ev.StanceID, ev.SkillRef}
			out.byStanceSkill[k] = append(out.byStanceSkill[k], ev)
		case "skill_unloaded":
			ev, err := unpackSkillUnloaded(n)
			if err != nil {
				continue
			}
			k := stanceSkillKey{ev.StanceID, ev.SkillRef}
			out.byStanceSkill[k] = append(out.byStanceSkill[k], ev)
		}
	}
	for k := range out.byStanceSkill {
		evs := out.byStanceSkill[k]
		sort.Slice(evs, func(i, j int) bool { return evs[i].At.Before(evs[j].At) })
		out.byStanceSkill[k] = evs
	}
	_ = sessionID
	return out, nil
}

func unpackSkillLoaded(n ledger.Node) (SkillEvent, error) {
	var sl nodes.SkillLoaded
	if err := json.Unmarshal(n.Content, &sl); err != nil {
		return SkillEvent{}, err
	}
	return SkillEvent{
		Type:       "skill_loaded",
		NodeID:     string(n.ID),
		SkillRef:   sl.SkillRef,
		StanceID:   sl.LoadingStanceID,
		StanceRole: sl.LoadingStanceRole,
		At:         sl.CreatedAt,
	}, nil
}

func unpackSkillUnloaded(n ledger.Node) (SkillEvent, error) {
	var su nodes.SkillUnloaded
	if err := json.Unmarshal(n.Content, &su); err != nil {
		return SkillEvent{}, err
	}
	return SkillEvent{
		Type:       "skill_unloaded",
		NodeID:     string(n.ID),
		SkillRef:   su.SkillRef,
		StanceID:   su.StanceID,
		StanceRole: su.StanceRole,
		LoadRef:    su.LoadRef,
		Reason:     su.Reason,
		Tokens:     su.BudgetTokensFreed,
		At:         su.CreatedAt,
	}, nil
}

// HumanSkillReason maps the SkillUnloaded.Reason enum onto operator-
// facing copy. Mirrors HumanReason in redaction.go.
func HumanSkillReason(reason string) string {
	switch reason {
	case "compactor_evicted":
		return "evicted by context compactor (budget pressure)"
	case "scope_exit":
		return "scope exited (task DAG closed)"
	case "explicit_unload":
		return "unloaded by operator"
	case "":
		return "(reason unknown)"
	}
	return "unloaded: " + reason
}

// ----- request-path cache -----

type skillMapCache struct {
	mu     sync.Mutex
	values map[string]skillMapCacheEntry
}

type skillMapCacheEntry struct {
	at  time.Time
	val *SkillEventMap
}

var skillCache = &skillMapCache{values: map[string]skillMapCacheEntry{}}

// LoadSkillEventMapCached is the request-path entry. 60s TTL.
func LoadSkillEventMapCached(store *ledger.Store, sessionID string) (*SkillEventMap, error) {
	skillCache.mu.Lock()
	defer skillCache.mu.Unlock()
	if e, ok := skillCache.values[sessionID]; ok && time.Since(e.at) < 60*time.Second {
		return e.val, nil
	}
	m, err := LoadSkillEventMap(store, sessionID)
	if err != nil {
		return nil, err
	}
	skillCache.values[sessionID] = skillMapCacheEntry{at: time.Now(), val: m}
	return m, nil
}
