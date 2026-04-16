// Package sharedmem implements STOKE-017: shared memory blocks with
// provenance for multi-agent collaboration. A SharedMemoryBlock is
// a typed, labeled, versioned datum that multiple agents can read
// and concurrently update under policy control, with every write
// carrying PROV-AGENT metadata (source agent, timestamp,
// confidence) and a reducer function resolving concurrent updates
// per the block's declared semantic.
//
// Three write semantics:
//
//  - Insert   (additive, concurrent-safe via reducer)
//  - Replace  (optimistic concurrency via expected version)
//  - Rethink  (last-writer-wins)
//
// Plus a Subscribe mechanism so agents receive updates on blocks
// they have read access to.
//
// Scope of this file: the Block struct, Store interface, in-memory
// implementation, and the three write semantics. Reducers are
// defined separately in reducer.go; subscription/provenance types
// are in subscribe.go / prov.go respectively so each concern is
// independently testable.
//
// Cedar policy enforcement is out of scope here — the Store takes
// a PolicyEnforcer interface so callers inject whatever evaluator
// they have (TrustPlane SDK in production; a mock in tests).
package sharedmem

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// BlockID is the stable identifier for a block. Typically a
// content hash derived from (Type, Label, initial value) at
// creation time.
type BlockID string

// BlockType is the semantic class of a block. Used by reducers +
// discovery UIs. Keep as a string so blocks can declare
// application-specific types without a registry.
type BlockType string

// Block is one entry in the shared-memory store. Carries its
// current Value as an opaque JSON-encodable any along with its
// schema metadata, version, provenance history, and policy ref.
type Block struct {
	ID      BlockID   `json:"id"`
	Type    BlockType `json:"type"`
	Label   string    `json:"label"`
	Version int       `json:"version"`

	// Value is the current content. Opaque to the store; the
	// reducer associated with Type is responsible for
	// interpreting it.
	Value any `json:"value"`

	// PolicyRef names a Cedar policy bundle (STOKE-015)
	// governing read/write access. Empty means "everyone with
	// namespace access can read + write" — suitable for
	// scratch blocks but not production.
	PolicyRef string `json:"policy_ref,omitempty"`

	// Namespace scopes visibility. Agents can only see blocks
	// in namespaces they have Cedar-granted access to
	// (STOKE-021). Defaults to "default".
	Namespace string `json:"namespace,omitempty"`

	// Provenance chain: one entry per write, ordered oldest to
	// newest. Never truncated by the store — callers that want
	// a shorter view should take a tail slice themselves.
	Provenance []ProvenanceEntry `json:"provenance"`

	// CreatedAt + UpdatedAt are the wall-clock timestamps of
	// first creation and latest write.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WriteSemantic tags a write with how concurrent updates should
// resolve. Insert uses the reducer; Replace uses expected-version
// optimistic concurrency; Rethink is last-writer-wins.
type WriteSemantic string

const (
	SemanticInsert  WriteSemantic = "insert"
	SemanticReplace WriteSemantic = "replace"
	SemanticRethink WriteSemantic = "rethink"
)

// Write captures a single mutation applied to a block. The Store
// turns a Write into a versioned append against the block's
// current state.
type Write struct {
	BlockID  BlockID
	Semantic WriteSemantic
	Value    any

	// ExpectedVersion is the caller-provided version number. For
	// SemanticReplace, the write fails if the block's current
	// Version doesn't match. Ignored for Insert / Rethink.
	ExpectedVersion int

	// Provenance is the PROV-AGENT metadata attached to this
	// write. Required for every write — a write without
	// provenance is rejected.
	Provenance ProvenanceEntry
}

// Store is the sharedmem interface. A real implementation is
// provided in-memory via MemoryStore; production deployments can
// drop in a backend-backed store (SQLite, KV service, etc.)
// implementing the same surface.
type Store interface {
	// Create inserts a new block. Fails if the block's ID
	// already exists. Caller supplies the full block shape
	// (Version=0, Provenance=[initial]); Create sets CreatedAt
	// and UpdatedAt.
	Create(ctx context.Context, block *Block) error

	// Get retrieves a block by ID. Returns ErrNotFound when
	// missing.
	Get(ctx context.Context, id BlockID) (*Block, error)

	// Apply runs a write against a block according to the
	// declared WriteSemantic. Returns the updated block on
	// success.
	Apply(ctx context.Context, w Write) (*Block, error)

	// Rollback restores a block to an earlier version (by
	// version number). The rollback itself is a write and
	// appears in Provenance so history is never lost — it's
	// just a new entry whose value mirrors an old one.
	Rollback(ctx context.Context, id BlockID, toVersion int, by ProvenanceEntry) (*Block, error)

	// Subscribe returns a channel that emits every future
	// update to the block. Closing the context closes the
	// channel.
	Subscribe(ctx context.Context, id BlockID) (<-chan *Block, error)
}

// ErrNotFound is returned by Store methods when a block doesn't
// exist.
var ErrNotFound = errors.New("sharedmem: block not found")

// ErrVersionMismatch is returned by SemanticReplace writes when
// the block's current version doesn't match the caller's
// ExpectedVersion. Callers retry by re-reading and re-computing.
var ErrVersionMismatch = errors.New("sharedmem: version mismatch")

// ErrAlreadyExists is returned by Create when the ID collides.
var ErrAlreadyExists = errors.New("sharedmem: block already exists")

// ErrNoProvenance is returned when a write doesn't carry PROV-AGENT
// metadata. Provenance is non-optional in this package — every
// write names its origin. Silent writes can happen in other
// abstractions, not here.
var ErrNoProvenance = errors.New("sharedmem: provenance is required")

// MemoryStore is the reference in-memory Store implementation.
// Thread-safe; every operation holds a write lock on the target
// block's entry plus a read lock on the store's top-level map.
type MemoryStore struct {
	mu       sync.Mutex
	blocks   map[BlockID]*Block
	subs     map[BlockID][]chan *Block
	reducers map[BlockType]Reducer
}

// NewMemoryStore returns a MemoryStore with no registered
// reducers and no blocks. Callers register reducers via
// RegisterReducer before Apply-ing Insert writes to a block of
// the matching type.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		blocks:   map[BlockID]*Block{},
		subs:     map[BlockID][]chan *Block{},
		reducers: map[BlockType]Reducer{},
	}
}

// RegisterReducer installs a reducer for a BlockType. Replaces
// any existing reducer for the same type. Callers can register
// custom reducers beyond the 3 built-ins (add / union / max).
func (s *MemoryStore) RegisterReducer(t BlockType, r Reducer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reducers[t] = r
}

// Create stores a new block. Fails if the ID is already present.
func (s *MemoryStore) Create(_ context.Context, b *Block) error {
	if b == nil {
		return fmt.Errorf("sharedmem: nil block")
	}
	if b.ID == "" {
		return fmt.Errorf("sharedmem: block id is required")
	}
	if err := validateProv(b.Provenance); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.blocks[b.ID]; exists {
		return ErrAlreadyExists
	}
	now := time.Now().UTC()
	b.CreatedAt = now
	b.UpdatedAt = now
	if b.Version == 0 {
		b.Version = 1
	}
	if b.Namespace == "" {
		b.Namespace = "default"
	}
	s.blocks[b.ID] = cloneBlock(b)
	return nil
}

// Get returns a clone of the block so callers can't mutate
// internal state by accident.
func (s *MemoryStore) Get(_ context.Context, id BlockID) (*Block, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.blocks[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneBlock(b), nil
}

// Apply runs the write according to its Semantic, emits a
// subscription event on success, and returns the new block
// state.
func (s *MemoryStore) Apply(_ context.Context, w Write) (*Block, error) {
	if err := validateProvEntry(w.Provenance); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.blocks[w.BlockID]
	if !ok {
		return nil, ErrNotFound
	}

	switch w.Semantic {
	case SemanticInsert:
		reducer, ok := s.reducers[b.Type]
		if !ok {
			return nil, fmt.Errorf("sharedmem: no reducer registered for type %q", b.Type)
		}
		next, err := reducer(b.Value, w.Value)
		if err != nil {
			return nil, fmt.Errorf("sharedmem: reducer failed: %w", err)
		}
		b.Value = next

	case SemanticReplace:
		if b.Version != w.ExpectedVersion {
			return nil, fmt.Errorf("%w: have %d want %d", ErrVersionMismatch, b.Version, w.ExpectedVersion)
		}
		b.Value = w.Value

	case SemanticRethink:
		b.Value = w.Value

	default:
		return nil, fmt.Errorf("sharedmem: unknown semantic %q", w.Semantic)
	}

	b.Version++
	b.UpdatedAt = time.Now().UTC()
	b.Provenance = append(b.Provenance, w.Provenance)

	s.emitUpdate(b)
	return cloneBlock(b), nil
}

// Rollback restores a block's Value to what it was at the given
// version. Implemented as a new write (not a destructive
// time-reversal) so history is preserved.
func (s *MemoryStore) Rollback(_ context.Context, id BlockID, toVersion int, by ProvenanceEntry) (*Block, error) {
	if err := validateProvEntry(by); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.blocks[id]
	if !ok {
		return nil, ErrNotFound
	}
	// Provenance ordering: the Nth entry corresponds to the
	// block state right after the Nth write. entry[0] is the
	// initial create (Version 1). Rolling back to Version V
	// means reconstructing the value as-of the Vth provenance
	// entry, which requires callers to have captured the value
	// in provenance. The minimal implementation walks the
	// provenance to find the entry whose resulting version
	// matches V. Since we don't store per-entry values here
	// (provenance is metadata only), we approximate: rollback
	// rewrites Value to the caller-supplied one in `by.ReplayValue`.
	// This keeps the package dependency-light; the wider
	// STOKE-017 implementation may add a value-tracking history
	// layer later.
	if by.ReplayValue == nil {
		return nil, fmt.Errorf("sharedmem: rollback requires by.ReplayValue (provide the target value)")
	}
	if toVersion <= 0 || toVersion >= b.Version {
		return nil, fmt.Errorf("sharedmem: rollback target %d out of range (current=%d)", toVersion, b.Version)
	}
	b.Value = by.ReplayValue
	b.Version++
	b.UpdatedAt = time.Now().UTC()
	rollbackEntry := by
	rollbackEntry.Action = "rollback"
	rollbackEntry.RolledBackTo = toVersion
	b.Provenance = append(b.Provenance, rollbackEntry)
	s.emitUpdate(b)
	return cloneBlock(b), nil
}

// Subscribe returns a channel that receives updates to id. The
// channel closes when ctx is canceled or when the block is
// deleted (future delete operation). Buffered to 4 slots so a
// slow subscriber can't block a writer — events are dropped
// rather than blocking the Apply.
func (s *MemoryStore) Subscribe(ctx context.Context, id BlockID) (<-chan *Block, error) {
	s.mu.Lock()
	if _, ok := s.blocks[id]; !ok {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	ch := make(chan *Block, 4)
	s.subs[id] = append(s.subs[id], ch)
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		subs := s.subs[id]
		for i, c := range subs {
			if c == ch {
				s.subs[id] = append(subs[:i], subs[i+1:]...)
				close(ch)
				return
			}
		}
	}()

	return ch, nil
}

// emitUpdate pushes a clone of b to every subscriber channel for
// the block. Drops events when a channel's buffer is full — slow
// subscribers lose messages but never block the write path.
// Caller must hold s.mu.
func (s *MemoryStore) emitUpdate(b *Block) {
	for _, ch := range s.subs[b.ID] {
		select {
		case ch <- cloneBlock(b):
		default:
			// buffer full — drop
		}
	}
}

// cloneBlock returns a shallow copy of b with Provenance slice
// copied so callers mutating the returned Block don't touch the
// stored one. Value is left as-is (any type) since deep-copying
// arbitrary any values is impractical without reflection; callers
// treating Value as read-only is the invariant.
func cloneBlock(b *Block) *Block {
	out := *b
	if len(b.Provenance) > 0 {
		out.Provenance = append([]ProvenanceEntry(nil), b.Provenance...)
	}
	return &out
}
