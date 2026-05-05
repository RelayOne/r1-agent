package ledger

// redact_log.go — issue #159: queryable per-node redaction-event log.
//
// The chain-tier already proves a node was redacted (chain entry
// present + content tier missing → IsRedacted returns true). What
// the ledger surface didn't have until now was a queryable history:
// "WHO redacted WHEN, and WHY?". Without that, the v2 dashboard's
// side panel renders every redacted node as the anomaly case
// ("Redacted, but no event log captured.") even when the operator
// did capture a reason at Redact() time.
//
// Storage: one append-only NDJSON file per node at
//
//   <root>/redactions/<nodeID>.ndjson
//
// Append-only is intentional. Redactions can compose (e.g. a
// retention-policy sweep followed by an explicit GDPR erasure of
// the same chain entry); preserving every entry chronologically
// lets the dashboard render the full audit trail.
//
// The signing path stays the responsibility of a higher layer — the
// log stores whatever fields the caller hands to RecordRedaction,
// including signer + signature_hex when those are populated.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SignedRedactionEvent extends RedactionRecord with the optional
// signer + signature_hex pair the encryption-at-rest spec carries.
// Wire-compatible with RedactionRecord (the extra fields are
// `omitempty` so an unsigned record round-trips cleanly).
type SignedRedactionEvent struct {
	NodeID       string `json:"node_id"`
	RedactedAt   string `json:"redacted_at"` // RFC3339Nano UTC
	Reason       string `json:"reason"`
	Signer       string `json:"signer,omitempty"`
	SignatureHex string `json:"signature_hex,omitempty"`
}

// redactionLogDirFor returns the per-store directory that holds the
// per-node NDJSON log files.
func (s *Store) redactionLogDirFor() string {
	return filepath.Join(filepath.Dir(s.nodesDir), "redactions")
}

// RecordRedaction appends one event to the node's log file. The
// caller is responsible for having actually performed the redaction
// (typically via Redact) — this function only persists the audit
// trail.
//
// Returns an error when:
//   - ev.NodeID is empty
//   - ev.RedactedAt is empty
//   - ev.Reason is empty (the audit trail is the whole point)
//   - the log dir cannot be created
//   - the file cannot be opened in append mode
//
// This function does NOT dedupe. Callers that want idempotent
// semantics should consult RedactionsFor first.
func (s *Store) RecordRedaction(ev SignedRedactionEvent) error {
	if ev.NodeID == "" {
		return errors.New("ledger redaction log: node_id is required")
	}
	if ev.RedactedAt == "" {
		return errors.New("ledger redaction log: redacted_at is required")
	}
	if ev.Reason == "" {
		return errors.New("ledger redaction log: reason is required")
	}
	dir := s.redactionLogDirFor()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ledger redaction log: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, ev.NodeID+".ndjson")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("ledger redaction log: open %s: %w", path, err)
	}
	defer f.Close()
	encoded, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("ledger redaction log: marshal: %w", err)
	}
	if _, err := f.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("ledger redaction log: write %s: %w", path, err)
	}
	return nil
}

// RedactionsFor returns every recorded SignedRedactionEvent for a
// node, sorted chronologically by RedactedAt. Returns nil + nil
// when the node has no log file (the IsRedacted-but-no-log anomaly
// case the v2 dashboard renders with the ⚠ overlay).
//
// nodeID is required. A read error other than os.ErrNotExist is
// surfaced; the absence of the log file itself is not an error.
func (s *Store) RedactionsFor(nodeID string) ([]SignedRedactionEvent, error) {
	if nodeID == "" {
		return nil, errors.New("ledger redaction log: nodeID is required")
	}
	path := filepath.Join(s.redactionLogDirFor(), nodeID+".ndjson")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("ledger redaction log: open %s: %w", path, err)
	}
	defer f.Close()

	var out []SignedRedactionEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev SignedRedactionEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// Skip malformed lines but surface the error so an
			// operator notices a corrupted log. Continue-on-
			// corruption over fail-closed because the log is
			// best-effort audit data and the alternative is the
			// side panel hiding every later valid entry.
			return out, fmt.Errorf("ledger redaction log: parse %s line %d: %w", path, lineNum, err)
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("ledger redaction log: scan %s: %w", path, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RedactedAt < out[j].RedactedAt })
	return out, nil
}

// RedactAndLog is the convenience wrapper that performs a Redact
// then immediately RecordRedaction with the returned record. This
// is the recommended path for callers that don't need to separate
// the redaction action from the audit-trail write (e.g. a retention-
// policy sweep). Signed-redaction callers that want to slot a
// signature in between should call Redact + RecordRedaction
// directly.
func (s *Store) RedactAndLog(ctx context.Context, nodeID, reason string) (SignedRedactionEvent, error) {
	rec, err := s.Redact(ctx, nodeID, reason)
	if err != nil {
		return SignedRedactionEvent{}, err
	}
	ev := SignedRedactionEvent{
		NodeID:     rec.NodeID,
		RedactedAt: rec.RedactedAt.UTC().Format(time.RFC3339Nano),
		Reason:     rec.Reason,
	}
	if err := s.RecordRedaction(ev); err != nil {
		return ev, err
	}
	return ev, nil
}
