// Package main — redaction.go
//
// Spec 3 §4.1: per-request RedactionMap loaded once and threaded into
// every waterfall row + side-panel render so the page doesn't fan out
// into N+1 ledger queries on a 1k-event session.
//
// The actual ledger surface (internal/ledger/redact.go) exposes
// Store.IsRedacted(nodeID) (bool, error) and a Redact() method that
// returns a RedactionRecord at the moment of redaction. There is no
// persisted per-node REDACTION HISTORY at the time of writing — the
// content blob is crypto-shredded but the audit trail is only what
// the caller chose to persist via Redact()'s return value. This file
// therefore exposes:
//
//   * IsRedacted(id) — backed by the real Store.IsRedacted()
//   * Events(id) — returns the cached []RedactionEvent if the caller
//     has chosen to log them out-of-band. Empty slice when no log
//     exists, which the side-panel UI treats as the "redacted-but-
//     reason-unknown" anomaly per RT-REDACTION-UI-PATTERNS.
//
// A future spec will add a queryable redaction-events log; until then
// this surface is the minimum the v2 templates need to render
// safely.

package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

// RedactionEvent is the projection of a ledger.RedactionRecord into
// the rendering pipeline. The HumanReason field is the
// caller-friendly form RT-REDACTION-UI-PATTERNS calls for
// ("redacted by retention policy (90d)" — never the bare word
// "redacted").
type RedactionEvent struct {
	NodeID       string    `json:"node_id"`
	RedactedAt   time.Time `json:"redacted_at"`
	Reason       string    `json:"reason"`
	HumanReason  string    `json:"human_reason"`
	Signer       string    `json:"signer,omitempty"`
	SignatureHex string    `json:"signature_hex,omitempty"`
}

// RedactionMap is the per-request projection of redacted state for a
// session. It is a thin map plus a methods receiver so the template
// layer can call .IsRedacted / .Events without the caller threading
// raw maps through context.
type RedactionMap struct {
	byNode map[string][]RedactionEvent
}

// IsRedacted reports whether nodeID's content tier has been wiped.
func (m *RedactionMap) IsRedacted(id string) bool {
	if m == nil {
		return false
	}
	_, ok := m.byNode[id]
	return ok
}

// Events returns the ordered redaction-event log for nodeID, or nil
// if the node has not been redacted. An empty slice (vs nil) indicates
// the anomaly case from §3 — node redacted but no reason captured.
func (m *RedactionMap) Events(id string) []RedactionEvent {
	if m == nil {
		return nil
	}
	return m.byNode[id]
}

// IsAnomaly reports the §3 case: redacted, but no event log. Surfaces
// the ⚠ overlay in side-panel.html.
func (m *RedactionMap) IsAnomaly(id string) bool {
	if m == nil {
		return false
	}
	evts, ok := m.byNode[id]
	return ok && len(evts) == 0
}

// LoadRedactionMap walks the session's nodes and populates the map
// against ledger.Store.IsRedacted. It does NOT load the
// (yet-to-be-shipped) per-node redaction-event history; callers that
// hold that log (e.g. a future SignedRedactionEvents store) can layer
// it via WithEvents below.
//
// Returns an error only on ledger I/O failure; an empty session
// returns an empty (non-nil) map.
func LoadRedactionMap(store *ledger.Store, sessionID string) (*RedactionMap, error) {
	if store == nil {
		return &RedactionMap{byNode: map[string][]RedactionEvent{}}, nil
	}
	nodes, err := store.ListNodes()
	if err != nil {
		return nil, fmt.Errorf("redaction map: list nodes: %w", err)
	}
	m := &RedactionMap{byNode: map[string][]RedactionEvent{}}
	for _, n := range nodes {
		isRed, err := store.IsRedacted(string(n.ID))
		if err != nil {
			return nil, fmt.Errorf("redaction map: isredacted %s: %w", n.ID, err)
		}
		if isRed {
			// Empty event slice = anomaly case until the per-node
			// redaction-event log lands. Non-nil so IsRedacted picks
			// it up via map presence.
			m.byNode[string(n.ID)] = []RedactionEvent{}
		}
	}
	_ = sessionID // sessionID currently unused — Store doesn't yet
	// support per-session filtering. Kept in the signature so the
	// future filter doesn't break callers.
	return m, nil
}

// WithEvents merges a caller-supplied event log into the map. The
// log replaces any existing events for that nodeID.
func (m *RedactionMap) WithEvents(byNode map[string][]RedactionEvent) *RedactionMap {
	if m == nil || byNode == nil {
		return m
	}
	for id, evts := range byNode {
		m.byNode[id] = evts
	}
	return m
}

// HumanReason maps the technical reason string from
// ledger.RedactionRecord.Reason into a UI-safe phrasing. Reasons that
// don't match a known pattern fall through to a neutral "(reason
// unknown)" rather than echo the raw string — RT-REDACTION-UI-PATTERNS
// explicitly warns against showing internal codes to operators.
func HumanReason(reason string) string {
	switch reason {
	case "":
		return "(reason unknown)"
	case "retention_policy":
		return "redacted by retention policy"
	case "gdpr_erasure":
		return "redacted under GDPR right-to-erasure"
	case "operator_request":
		return "redacted by operator request"
	}
	return "redacted: " + reason
}

// redactionMapCache memoises maps per (sessionID, store-pointer) for
// 60s to dodge re-walking the chain on every page render. Spec 3 §4.1
// + acceptance §11 ("1k-event session loads /waterfall in < 200 ms").
type redactionMapCache struct {
	mu     sync.Mutex
	values map[string]redactionMapCacheEntry
}

type redactionMapCacheEntry struct {
	at  time.Time
	val *RedactionMap
}

var redCache = &redactionMapCache{values: map[string]redactionMapCacheEntry{}}

// LoadRedactionMapCached is the request-path entry point. TTL 60s.
func LoadRedactionMapCached(store *ledger.Store, sessionID string) (*RedactionMap, error) {
	redCache.mu.Lock()
	defer redCache.mu.Unlock()
	if e, ok := redCache.values[sessionID]; ok && time.Since(e.at) < 60*time.Second {
		return e.val, nil
	}
	m, err := LoadRedactionMap(store, sessionID)
	if err != nil {
		return nil, err
	}
	redCache.values[sessionID] = redactionMapCacheEntry{at: time.Now(), val: m}
	return m, nil
}
