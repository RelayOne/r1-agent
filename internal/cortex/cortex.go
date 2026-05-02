// Package cortex implements a Global-Workspace-Theory (GWT) inspired
// cognitive architecture for the agent. Inspired by mammalian cortex
// dynamics, it coordinates a set of parallel Lobes -- cognitive
// specialists that each receive the full conversation context and
// reason concurrently -- around a shared Workspace. Execution proceeds
// in discrete superstep Rounds: every Lobe runs in parallel against a
// snapshot of the Workspace, a barrier collects their proposals, a
// Spotlight selector elevates the most salient contribution into the
// next-round Workspace, and a Router lets the agent decide how each
// proposal merges back (broadcast, addressed, or dropped). This avoids
// the term used by internal/concern (which handles per-stance context
// projection) and instead uses Lobe/Workspace/Spotlight/Router as the
// load-bearing vocabulary. See specs/research/synthesized/cortex.md for
// the GWT background and specs/cortex-core.md for the build plan.
package cortex

import "errors"

// Config carries cortex construction parameters. Fields are introduced
// in TASK-12; this skeleton exists so dependents can compile-link.
type Config struct{}

// Cortex is the runtime handle returned by New. Fields are introduced
// in TASK-12; this skeleton exists so dependents can compile-link.
type Cortex struct{}

// New constructs a Cortex from the given Config. Until TASK-12 wires up
// the Workspace/Lobes/Round/Spotlight/Router runtime, it returns an
// error so callers fail fast at startup rather than at first use.
func New(cfg Config) (*Cortex, error) {
	return nil, errors.New("cortex: not implemented (TASK-12)")
}
