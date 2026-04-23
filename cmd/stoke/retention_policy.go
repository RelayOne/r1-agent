// Package main — retention_policy.go
//
// work-stoke T10 glue: builds the per-run retention.Policy consumed by
// the retention enforcement hooks (retention.EnforceOnSessionEnd,
// retention.EnforceSweep).
//
// The --retention-permanent flag on `stoke sow` flips every operator-
// configurable surface to RetainForever for the duration of this run.
// That makes the session-end ephemeral wipe and the hourly sweep
// no-ops, which is what audit / compliance runs want: the full memory +
// stream + checkpoint trail is preserved regardless of the default
// policy's TTLs.
//
// Kept in a standalone file so it does not cross-contaminate
// sow_native.go's 6000-line config struct; the policy is stashed in
// a package-level var rather than a new sowNativeConfig field so the
// existing struct surface stays stable. activeRetentionPolicy is
// process-scoped and safe to read from any goroutine — sowCmd is a
// single-threaded setup path that assigns it once before any worker
// dispatch, and every reader (the future session-end hook) is a
// read-only SELECT-like access.

package main

import (
	"sync"

	"github.com/ericmacdougall/stoke/internal/retention"
)

// activeRetentionPolicy holds the retention.Policy chosen for this
// process lifetime (normally one `stoke sow` run). sowCmd populates it
// once during flag parsing via setRetentionPolicy. Consumers read it
// via retentionPolicy(). Guarded by a mutex even though contention is
// functionally zero so race-detector runs stay clean.
var (
	activeRetentionPolicyMu sync.RWMutex
	activeRetentionPolicy   = retention.Defaults()
)

// buildRetentionPolicy returns the retention.Policy for this run. When
// permanent is false the caller gets the spec's default profile
// (specs/retention-policies.md §4). When true every operator-
// configurable surface is pinned to RetainForever so the session-end
// ephemeral wipe and hourly sweep become no-ops for the duration of
// this run — useful for audit runs where the full memory + stream +
// checkpoint trail must be preserved. The immutable-forever fields
// (permanent_memories, ledger_nodes) stay RetainForever either way;
// flipping them would fail Policy.Validate().
func buildRetentionPolicy(permanent bool) retention.Policy {
	p := retention.Defaults()
	if !permanent {
		return p
	}
	p.EphemeralMemories = retention.RetainForever
	p.SessionMemories = retention.RetainForever
	p.PersistentMemories = retention.RetainForever
	p.StreamFiles = retention.RetainForever
	p.LedgerContent = retention.RetainForever
	p.CheckpointFiles = retention.RetainForever
	p.PromptsAndResponses = retention.RetainForever
	return p
}

// setRetentionPolicy stores the run-scoped policy. sowCmd calls this
// once after parsing --retention-permanent; downstream retention hooks
// read it back via retentionPolicy(). No-op on a zero-value policy so
// a caller that skips the flag-parse path leaves the default policy
// in place.
func setRetentionPolicy(p retention.Policy) {
	if err := p.Validate(); err != nil {
		// Zero-value / invalid policies are silently ignored — the
		// default profile stays in effect so retention enforcement
		// still has a sane configuration to operate against.
		return
	}
	activeRetentionPolicyMu.Lock()
	activeRetentionPolicy = p
	activeRetentionPolicyMu.Unlock()
}

// retentionPolicy returns the active retention policy. Safe to call
// from any goroutine; the default profile is returned before sowCmd
// has had a chance to populate the override.
func retentionPolicy() retention.Policy {
	activeRetentionPolicyMu.RLock()
	defer activeRetentionPolicyMu.RUnlock()
	return activeRetentionPolicy
}
