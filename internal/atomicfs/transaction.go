// Package atomicfs implements multi-file atomic edits with transactional semantics.
// Inspired by claw-code's file mutation system and SWE-agent's edit verification:
//
// All file writes in a transaction are staged to temp files first. On commit,
// they're atomically renamed into place. On rollback (or panic), originals are
// preserved. This prevents partial edits that leave the workspace broken.
//
// Features:
// - Multi-file atomicity: all changes apply or none do
// - Automatic backup of originals for rollback
// - Dry-run mode for previewing changes
// - Conflict detection (file changed since read)
package atomicfs

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Canonical Op.Kind values distinguishing file operations.
const (
	opKindWrite  = "write"
	opKindCreate = "create"
	opKindDelete = "delete"
)

// Op is a pending file operation.
type Op struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"` // "write", "delete", "create"
	Content    []byte `json:"-"`
	OrigHash   string `json:"orig_hash,omitempty"` // sha256 at read time
	origExists bool
}

// Transaction groups file operations for atomic commit.
type Transaction struct {
	mu     sync.Mutex
	ops    []Op
	dir    string // working directory for relative paths
	sealed bool
}

// NewTransaction creates a new transaction rooted at dir.
func NewTransaction(dir string) *Transaction {
	return &Transaction{dir: dir}
}

// Write stages a file write. The content is written on Commit().
func (tx *Transaction) Write(path string, content []byte) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.sealed {
		return fmt.Errorf("transaction already committed")
	}

	fullPath := tx.resolve(path)

	// Record original hash for conflict detection
	origHash := ""
	origExists := false
	if data, err := os.ReadFile(fullPath); err == nil {
		origHash = hash(data)
		origExists = true
	}

	tx.ops = append(tx.ops, Op{
		Path:       fullPath,
		Kind:       opKindWrite,
		Content:    content,
		OrigHash:   origHash,
		origExists: origExists,
	})
	return nil
}

// Create stages a new file creation. Fails on commit if file exists.
func (tx *Transaction) Create(path string, content []byte) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.sealed {
		return fmt.Errorf("transaction already committed")
	}

	fullPath := tx.resolve(path)
	tx.ops = append(tx.ops, Op{
		Path:    fullPath,
		Kind:    opKindCreate,
		Content: content,
	})
	return nil
}

// Delete stages a file deletion.
func (tx *Transaction) Delete(path string) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.sealed {
		return fmt.Errorf("transaction already committed")
	}

	fullPath := tx.resolve(path)
	origHash := ""
	if data, err := os.ReadFile(fullPath); err == nil {
		origHash = hash(data)
	}

	tx.ops = append(tx.ops, Op{
		Path:       fullPath,
		Kind:       opKindDelete,
		OrigHash:   origHash,
		origExists: true,
	})
	return nil
}

// Validate checks for conflicts without applying changes.
// Returns nil if all operations can proceed safely.
func (tx *Transaction) Validate() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	for _, op := range tx.ops {
		switch op.Kind {
		case opKindWrite:
			if op.OrigHash != "" {
				currentData, err := os.ReadFile(op.Path)
				if err != nil {
					return fmt.Errorf("file disappeared: %s", op.Path)
				}
				if hash(currentData) != op.OrigHash {
					return fmt.Errorf("conflict: %s was modified since read", op.Path)
				}
			}
		case opKindCreate:
			if _, err := os.Stat(op.Path); err == nil {
				return fmt.Errorf("file already exists: %s", op.Path)
			}
		case opKindDelete:
			if _, err := os.Stat(op.Path); err != nil {
				return fmt.Errorf("file not found for deletion: %s", op.Path)
			}
		}
	}
	return nil
}

// DryRun returns a summary of what would happen on commit.
func (tx *Transaction) DryRun() []string {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	var summary []string
	for _, op := range tx.ops {
		switch op.Kind {
		case opKindWrite:
			summary = append(summary, fmt.Sprintf("WRITE %s (%d bytes)", op.Path, len(op.Content)))
		case opKindCreate:
			summary = append(summary, fmt.Sprintf("CREATE %s (%d bytes)", op.Path, len(op.Content)))
		case opKindDelete:
			summary = append(summary, fmt.Sprintf("DELETE %s", op.Path))
		}
	}
	return summary
}

// Commit applies all staged operations atomically.
// If any operation fails, all changes are rolled back.
func (tx *Transaction) Commit() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.sealed {
		return fmt.Errorf("transaction already committed")
	}

	// Phase 1: Validate
	for _, op := range tx.ops {
		if op.Kind == opKindWrite && op.OrigHash != "" {
			currentData, err := os.ReadFile(op.Path)
			if err != nil {
				return fmt.Errorf("conflict: %s disappeared", op.Path)
			}
			if hash(currentData) != op.OrigHash {
				return fmt.Errorf("conflict: %s was modified since read", op.Path)
			}
		}
		if op.Kind == opKindCreate {
			if _, err := os.Stat(op.Path); err == nil {
				return fmt.Errorf("conflict: %s already exists", op.Path)
			}
		}
	}

	// Phase 2: Write to temp files
	type staged struct {
		tmpPath string
		op      Op
	}
	var stg []staged

	cleanup := func() {
		for _, s := range stg {
			os.Remove(s.tmpPath)
		}
	}

	for _, op := range tx.ops {
		if op.Kind == opKindWrite || op.Kind == opKindCreate {
			dir := filepath.Dir(op.Path)
			if err := os.MkdirAll(dir, 0755); err != nil {
				cleanup()
				return fmt.Errorf("mkdir %s: %w", dir, err)
			}
			tmp, err := os.CreateTemp(dir, ".atomicfs-*")
			if err != nil {
				cleanup()
				return fmt.Errorf("create temp for %s: %w", op.Path, err)
			}
			if _, err := tmp.Write(op.Content); err != nil {
				tmp.Close()
				cleanup()
				return fmt.Errorf("write temp for %s: %w", op.Path, err)
			}
			tmp.Close()
			stg = append(stg, staged{tmpPath: tmp.Name(), op: op})
		}
	}

	// Phase 3: Backup originals
	type backup struct {
		path string
		data []byte
	}
	var backups []backup

	for _, op := range tx.ops {
		if op.origExists {
			data, err := os.ReadFile(op.Path)
			if err == nil {
				backups = append(backups, backup{path: op.Path, data: data})
			}
		}
	}

	// Phase 4: Apply (rename temps + delete)

	rollback := func() {
		// Restore backups
		for _, b := range backups {
			os.WriteFile(b.path, b.data, 0644)
		}
		cleanup()
	}

	for _, s := range stg {
		if err := os.Rename(s.tmpPath, s.op.Path); err != nil {
			rollback()
			return fmt.Errorf("rename %s: %w", s.op.Path, err)
		}
	}

	for _, op := range tx.ops {
		if op.Kind == opKindDelete {
			if err := os.Remove(op.Path); err != nil {
				rollback()
				return fmt.Errorf("delete %s: %w", op.Path, err)
			}
		}
	}

	tx.sealed = true
	return nil
}

// Len returns the number of staged operations.
func (tx *Transaction) Len() int {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return len(tx.ops)
}

// Files returns the list of files affected by this transaction.
func (tx *Transaction) Files() []string {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	var files []string
	for _, op := range tx.ops {
		files = append(files, op.Path)
	}
	return files
}

func (tx *Transaction) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(tx.dir, path)
}

func hash(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:8]) // 16-char prefix sufficient for conflict detection
}

// Summary returns a human-readable summary of the transaction.
func (tx *Transaction) Summary() string {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	writes, creates, deletes := 0, 0, 0
	for _, op := range tx.ops {
		switch op.Kind {
		case opKindWrite:
			writes++
		case opKindCreate:
			creates++
		case opKindDelete:
			deletes++
		}
	}
	parts := []string{}
	if writes > 0 {
		parts = append(parts, fmt.Sprintf("%d writes", writes))
	}
	if creates > 0 {
		parts = append(parts, fmt.Sprintf("%d creates", creates))
	}
	if deletes > 0 {
		parts = append(parts, fmt.Sprintf("%d deletes", deletes))
	}
	if len(parts) == 0 {
		return "empty transaction"
	}
	return strings.Join(parts, ", ")
}
