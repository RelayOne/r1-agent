// Audit-log writer for MemoryCuratorLobe (spec item 30).
//
// Each auto-write is appended to PrivacyConfig.AuditLogPath as a single
// JSON line. The schema is the spec §6 "Audit trail" contract:
//
//	{"ts": RFC3339, "entry_id": ..., "category": ..., "content_sha": ...,
//	 "source_msg_id": ..., "decision": "auto-applied"}
//
// Plus a "content" field carrying the (possibly truncated) entry body —
// the user-spec adaptation block for TASK-30 calls for that field
// explicitly so `r1 cortex memory audit` can pretty-print without a
// second lookup into memory.Store.
//
// The writer opens AuditLogPath in append mode for each write so the
// daemon does not pin a file descriptor across restarts and so tests
// can swap the path mid-run by mutating PrivacyConfig.
package memorycurator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AuditEntry is the JSON shape of one audit-log line. Exported so the
// CLI command in cmd/r1/cortex_memory_audit.go (TASK-31) can decode
// without duplicating the field set.
type AuditEntry struct {
	Timestamp   string `json:"timestamp"`
	EntryID     string `json:"entry_id,omitempty"`
	Category    string `json:"category"`
	Content     string `json:"content"`
	ContentSHA  string `json:"content_sha"`
	SourceMsgID string `json:"source_msg_id,omitempty"`
	Decision    string `json:"decision"`
}

// appendAuditLine appends one AuditEntry to path as a JSONL record. The
// directory is created (mkdir -p) if it does not already exist; the file
// is opened in O_APPEND|O_CREATE mode with 0600 perms so audit content
// (which may include partial conversation text) is owner-only.
//
// Returns a non-nil error on IO failure; the caller logs and proceeds —
// audit-log failures must not block the auto-write itself per spec
// (the memory entry is the primary side effect).
func appendAuditLine(path string, ent AuditEntry) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("audit: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("audit: open: %w", err)
	}
	defer f.Close()

	line, err := json.Marshal(ent)
	if err != nil {
		return fmt.Errorf("audit: marshal: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("audit: write: %w", err)
	}
	return nil
}

// newAuditEntry constructs an AuditEntry stamped with the current time
// and the SHA-256 of the content. Centralised so every code path that
// writes the audit log fills the same fields the same way.
func newAuditEntry(category, content, entryID, sourceMsgID, decision string) AuditEntry {
	sum := sha256.Sum256([]byte(content))
	return AuditEntry{
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		EntryID:     entryID,
		Category:    category,
		Content:     content,
		ContentSHA:  hex.EncodeToString(sum[:]),
		SourceMsgID: sourceMsgID,
		Decision:    decision,
	}
}
