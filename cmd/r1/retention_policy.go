// Package main — retention_policy.go
//
// work-stoke T10 glue: builds the per-run retention.Policy consumed by
// the session-end retention enforcement hook
// (retention.EnforceOnSessionEnd, wired into emitSessionEnd in
// sow_native_streamjson.go). The hourly sweep half
// (retention.EnforceSweep) runs in the separate r1-server process
// with its own defaults and does NOT read this register.
//
// The --retention-permanent flag on `r1 sow` flips every operator-
// configurable surface to RetainForever for the duration of this run.
// That makes the session-end ephemeral wipe a no-op, which is what
// audit / compliance runs want: the full memory + stream + checkpoint
// trail is preserved regardless of the default policy's TTLs.
//
// Enforcement is additionally gated on STOKE_RETENTION=1 per
// specs/retention-policies.md §Flag gate — when the env var is unset
// (the default) no session-end wipe runs at all, preserving the
// historical retain-forever behavior.

package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/RelayOne/r1/internal/memory/membus"
	"github.com/RelayOne/r1/internal/retention"
)

// runRetentionPolicy is the package-level register holding the per-run
// retention policy — the single source of truth the session-end
// enforcement hook reads. sowCmd writes it once at flag-parse time via
// setRetentionPolicy; enforceRetentionOnSessionEnd reads it via
// currentRetentionPolicy. The mutex is cheap insurance for reads from
// session goroutines that outlive the flag-parse write.
var (
	runRetentionMu     sync.RWMutex
	runRetentionPolicy = retention.Defaults()
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

// setRetentionPolicy validates the run-scoped policy and stores it in
// the package-level register. sowCmd calls this once after parsing
// --retention-permanent. Fail-soft: an invalid policy is discarded so
// the default profile stays in effect rather than aborting the run.
func setRetentionPolicy(p retention.Policy) {
	if err := p.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "warn: retention policy rejected, keeping defaults: %v\n", err)
		return
	}
	runRetentionMu.Lock()
	runRetentionPolicy = p
	runRetentionMu.Unlock()
}

// currentRetentionPolicy returns the run-scoped retention policy set
// by setRetentionPolicy, defaulting to retention.Defaults() when the
// flag was never parsed (e.g. non-sow commands).
func currentRetentionPolicy() retention.Policy {
	runRetentionMu.RLock()
	defer runRetentionMu.RUnlock()
	return runRetentionPolicy
}

// enforceRetentionOnSessionEnd runs the session-close retention pass
// (specs/retention-policies.md item 32) against the run-scoped policy.
// Gated on STOKE_RETENTION=1 per the spec's §Flag gate: when unset the
// engine stays retain-forever and this is a no-op. Errors are logged
// and never returned — the session-close path MUST NOT fail on
// retention problems. Nil-safe on bus (legacy invocations without a
// memory bus skip enforcement inside EnforceOnSessionEnd).
func enforceRetentionOnSessionEnd(ctx context.Context, sessionID string, bus *membus.Bus) {
	if os.Getenv("STOKE_RETENTION") != "1" {
		return
	}
	if err := retention.EnforceOnSessionEnd(ctx, currentRetentionPolicy(), sessionID, bus); err != nil {
		fmt.Fprintf(os.Stderr, "warn: retention session-end enforcement (%s): %v\n", sessionID, err)
	}
}
