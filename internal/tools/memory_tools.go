// memory_tools.go — memory_store, memory_recall, memory_forget tool handlers.
//
// T-R1P-013: Agent memory tools — persistent cross-session knowledge storage.
// Wraps internal/memory.Store as an agent-facing tool so models can store
// learnings, recall relevant context, and prune stale entries.
//
// Storage: .r1/agent-memory.json under the registry working directory.
// The memory store is loaded lazily on first use and saved after every write.
package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/memory"
)

// toolMemoryStore wraps memory.Store for the tools layer.
type toolMemoryStore struct {
	store *memory.Store
}

// lazyMem returns the registry's toolMemoryStore, initialising it on first call.
func (r *Registry) lazyMem() *toolMemoryStore {
	r.memOnce.Do(func() {
		path := ""
		if r.workDir != "" {
			path = filepath.Join(r.workDir, ".r1", "agent-memory.json")
		}
		s, err := memory.NewStore(memory.Config{Path: path})
		if err != nil {
			// Fallback to in-memory only if the path is unwritable.
			s, _ = memory.NewStore(memory.Config{})
		}
		r.memStore = &toolMemoryStore{store: s}
	})
	return r.memStore
}

// handleMemoryStore implements memory_store (T-R1P-013).
func (r *Registry) handleMemoryStore(input json.RawMessage) (string, error) {
	var args struct {
		Content  string   `json:"content"`
		Category string   `json:"category"`
		Tags     []string `json:"tags"`
		File     string   `json:"file"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Content) == "" {
		return "", fmt.Errorf("content is required")
	}

	cat := memory.Category(args.Category)
	switch cat {
	case memory.CatGotcha, memory.CatPattern, memory.CatPreference,
		memory.CatFact, memory.CatAntiPattern, memory.CatFix:
	default:
		cat = memory.CatFact
	}

	mem := r.lazyMem()
	var entry *memory.Entry
	if args.File != "" {
		entry = mem.store.RememberWithContext(cat, args.Content, "", args.File, args.Tags...)
	} else {
		entry = mem.store.Remember(cat, args.Content, args.Tags...)
	}

	if err := mem.store.Save(); err != nil {
		// Non-fatal: memory stored in-process even if disk write fails.
		return fmt.Sprintf("Stored memory %s (warning: save failed: %v)", entry.ID, err), nil
	}
	return fmt.Sprintf("Stored memory %s [%s]", entry.ID, cat), nil
}

// handleMemoryRecall implements memory_recall (T-R1P-013).
func (r *Registry) handleMemoryRecall(input json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("query is required")
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	mem := r.lazyMem()
	entries := mem.store.Recall(args.Query, limit)

	if len(entries) == 0 {
		return "(no relevant memories found)", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d memories matching %q:\n\n", len(entries), args.Query)
	for _, e := range entries {
		fmt.Fprintf(&sb, "[%s] (%s) %s", e.ID, e.Category, e.Content)
		if e.File != "" {
			fmt.Fprintf(&sb, " (re: %s)", e.File)
		}
		if len(e.Tags) > 0 {
			fmt.Fprintf(&sb, " [%s]", strings.Join(e.Tags, ", "))
		}
		fmt.Fprintf(&sb, " (used %d times, confidence %.0f%%)\n", e.UseCount, e.Confidence*100)
		// Mark as used so scoring improves over time.
		mem.store.MarkUsed(e.ID)
	}
	return sb.String(), nil
}

// handleMemoryForget implements memory_forget (T-R1P-013).
func (r *Registry) handleMemoryForget(input json.RawMessage) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if args.ID == "" {
		return "", fmt.Errorf("id is required")
	}

	mem := r.lazyMem()
	before := mem.store.Count()
	mem.store.Forget(args.ID)
	after := mem.store.Count()
	if after == before {
		return "", fmt.Errorf("memory with id %q not found", args.ID)
	}
	if err := mem.store.Save(); err != nil {
		return fmt.Sprintf("Deleted memory %s (warning: save failed: %v)", args.ID, err), nil
	}
	return fmt.Sprintf("Deleted memory %s", args.ID), nil
}
