package nodes

import (
	"fmt"
	"time"
)

// MemoryStored records that a worker wrote a memory row via the memory bus
// (internal/memory/membus). The ledger holds provenance only — the raw
// content lives in SQLite, and the ledger references it by content_hash so
// auditors can correlate writes without the payload leaking. See
// specs/memory-bus.md §9.
// ID prefix: mst-
type MemoryStored struct {
	Scope       string `json:"scope"`
	ScopeTarget string `json:"scope_target,omitempty"`
	Key         string `json:"key"`
	ContentHash string `json:"content_hash"`
	MemoryType  string `json:"memory_type,omitempty"`
	WrittenBy   string `json:"written_by"`

	CreatedAt time.Time `json:"created_at"`
	Version   int       `json:"schema_version"`
}

func (m *MemoryStored) NodeType() string   { return "memory_stored" }
func (m *MemoryStored) SchemaVersion() int { return m.Version }

func (m *MemoryStored) Validate() error {
	if m.Scope == "" {
		return fmt.Errorf("memory_stored: scope is required")
	}
	if m.Key == "" {
		return fmt.Errorf("memory_stored: key is required")
	}
	if m.ContentHash == "" {
		return fmt.Errorf("memory_stored: content_hash is required")
	}
	if m.WrittenBy == "" {
		return fmt.Errorf("memory_stored: written_by is required")
	}
	if m.CreatedAt.IsZero() {
		return fmt.Errorf("memory_stored: created_at is required")
	}
	return nil
}

// MemoryRecalled records that a worker read a memory row. Like MemoryStored
// it references the payload by content_hash so the ledger never contains the
// recalled content itself.
// ID prefix: mrc-
type MemoryRecalled struct {
	Scope       string `json:"scope"`
	Key         string `json:"key"`
	ContentHash string `json:"content_hash"`
	RecalledBy  string `json:"recalled_by"`

	CreatedAt time.Time `json:"created_at"`
	Version   int       `json:"schema_version"`
}

func (m *MemoryRecalled) NodeType() string   { return "memory_recalled" }
func (m *MemoryRecalled) SchemaVersion() int { return m.Version }

func (m *MemoryRecalled) Validate() error {
	if m.Scope == "" {
		return fmt.Errorf("memory_recalled: scope is required")
	}
	if m.Key == "" {
		return fmt.Errorf("memory_recalled: key is required")
	}
	if m.ContentHash == "" {
		return fmt.Errorf("memory_recalled: content_hash is required")
	}
	if m.RecalledBy == "" {
		return fmt.Errorf("memory_recalled: recalled_by is required")
	}
	if m.CreatedAt.IsZero() {
		return fmt.Errorf("memory_recalled: created_at is required")
	}
	return nil
}

func init() {
	Register("memory_stored", func() NodeTyper { return &MemoryStored{Version: 1} })
	Register("memory_recalled", func() NodeTyper { return &MemoryRecalled{Version: 1} })
}
