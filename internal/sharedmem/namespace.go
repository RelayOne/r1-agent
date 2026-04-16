// Package sharedmem — namespace.go
//
// STOKE-021 privacy isolation: namespace-scoped memory partitions
// on top of the Store. Each Block declares a Namespace field; a
// NamespacedStore wraps any Store and filters every read +
// subscribe through a per-caller allow-list so an agent only sees
// blocks in namespaces its delegation grants.
//
// This is orthogonal to Cedar policy enforcement (which happens
// at tool-invocation boundaries). Namespace scoping is the
// coarser grain: "which blocks is this agent even allowed to
// see?" — evaluated before Cedar decides "which operations on
// this block is this agent allowed to perform?".
//
// Compositional inference monitoring (the SOW's third bullet)
// isn't in this file — it needs cross-block accumulation tracking
// that's stateful per session. The hook for it is the
// InferenceMonitor interface; callers can inject an implementation
// that flags suspicious cross-namespace context accumulation.
package sharedmem

import (
	"context"
	"errors"
	"fmt"
)

// NamespaceAllowList is the set of namespaces a caller has read
// access to. An empty list means "default namespace only" (the
// narrowest default); nil means "no access" (not an empty slice
// — callers must opt in).
type NamespaceAllowList struct {
	namespaces map[string]struct{}
}

// NewAllowList returns an allow-list covering the provided
// namespaces. Pass ["default"] to match the implicit default
// namespace only.
func NewAllowList(ns ...string) NamespaceAllowList {
	m := make(map[string]struct{}, len(ns))
	for _, n := range ns {
		if n == "" {
			n = "default"
		}
		m[n] = struct{}{}
	}
	return NamespaceAllowList{namespaces: m}
}

// Allows reports whether ns is in the allow-list. The empty
// string is normalized to "default" (matching Block.Namespace's
// default).
func (a NamespaceAllowList) Allows(ns string) bool {
	if ns == "" {
		ns = "default"
	}
	_, ok := a.namespaces[ns]
	return ok
}

// List returns the sorted namespace list. Used by reports +
// debugging.
func (a NamespaceAllowList) List() []string {
	out := make([]string, 0, len(a.namespaces))
	for n := range a.namespaces {
		out = append(out, n)
	}
	return out
}

// ErrNamespaceDenied is returned by NamespacedStore when the
// caller's allow-list doesn't cover the block's namespace.
var ErrNamespaceDenied = errors.New("sharedmem: namespace access denied")

// InferenceMonitor is the hook for compositional-inference
// monitoring (SOW-STOKE-021 third bullet). Implementations track
// which namespaces a caller has read over a window of time and
// flag accumulation patterns that would reveal cross-namespace
// sensitive attributes (e.g. reading "compensation.amount" from
// namespace A and "employee.role" from namespace B within the
// same session accumulates into PII-adjacent context).
//
// A nil InferenceMonitor disables the check entirely; real
// deployments inject one.
type InferenceMonitor interface {
	// RecordRead records that caller read the named block.
	// Returns an error to fail the read when accumulation
	// exceeds policy; nil to allow.
	RecordRead(ctx context.Context, callerID string, block *Block) error
}

// NamespacedStore wraps a Store with per-caller namespace
// filtering + optional compositional-inference monitoring. Each
// read passes through NamespaceAllowList (rejected with
// ErrNamespaceDenied on miss) and then through InferenceMonitor
// (if non-nil).
type NamespacedStore struct {
	inner    Store
	monitor  InferenceMonitor
}

// NewNamespacedStore wraps `inner`. Monitor may be nil (no
// compositional-inference check).
func NewNamespacedStore(inner Store, monitor InferenceMonitor) *NamespacedStore {
	return &NamespacedStore{inner: inner, monitor: monitor}
}

// Create passes through — blocks must declare their namespace at
// create time. No allow-list check here because the creator
// inherently has write access to the namespace it's declaring;
// cross-namespace creation is enforced at the Cedar layer, not
// here.
func (n *NamespacedStore) Create(ctx context.Context, b *Block) error {
	return n.inner.Create(ctx, b)
}

// Get returns the block only if callerID's allow-list covers
// the block's namespace. Returns ErrNamespaceDenied otherwise.
// The inference monitor runs after the allow-list check so
// denied reads don't pollute accumulation counters.
func (n *NamespacedStore) Get(ctx context.Context, id BlockID, callerID string, allow NamespaceAllowList) (*Block, error) {
	b, err := n.inner.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !allow.Allows(b.Namespace) {
		return nil, fmt.Errorf("%w: %q not in allow-list %v", ErrNamespaceDenied, b.Namespace, allow.List())
	}
	if n.monitor != nil {
		if err := n.monitor.RecordRead(ctx, callerID, b); err != nil {
			return nil, fmt.Errorf("sharedmem: inference monitor denied: %w", err)
		}
	}
	return b, nil
}

// Apply writes only succeed if the block's namespace is in the
// caller's allow-list. Cedar rules at the calling layer decide
// which OPERATIONS on the block are allowed; this check is the
// coarser "does the caller even see this block?" gate.
func (n *NamespacedStore) Apply(ctx context.Context, w Write, callerID string, allow NamespaceAllowList) (*Block, error) {
	// Read-check before write so we don't leak existence of
	// blocks the caller shouldn't see.
	b, err := n.inner.Get(ctx, w.BlockID)
	if err != nil {
		return nil, err
	}
	if !allow.Allows(b.Namespace) {
		// Uniform error: don't distinguish "block exists but
		// you can't see it" from "block doesn't exist". Keeps
		// existence an information-theoretic secret.
		return nil, fmt.Errorf("%w: block %q", ErrNamespaceDenied, w.BlockID)
	}
	return n.inner.Apply(ctx, w)
}

// Subscribe returns an update channel only when the caller can
// see the block. If the block's namespace changes (unlikely — we
// don't support that) the subscription would need re-auth; this
// package doesn't currently support namespace mutation so the
// check happens once at subscribe time.
func (n *NamespacedStore) Subscribe(ctx context.Context, id BlockID, callerID string, allow NamespaceAllowList) (<-chan *Block, error) {
	b, err := n.inner.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !allow.Allows(b.Namespace) {
		return nil, fmt.Errorf("%w: block %q", ErrNamespaceDenied, id)
	}
	return n.inner.Subscribe(ctx, id)
}
