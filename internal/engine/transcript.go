// transcript.go — event-sourced JSONL transcript of the native
// agentloop's full conversation.
//
// The agentloop already carries the complete message history in
// Result.Messages, but nothing persisted it: the worker log truncates
// tool input/result at 4KB and hub events cap tool output at 1KB, so
// no artifact could serve as a lossless replay/resume source. The
// transcript closes that gap with one append-only JSONL file per
// dispatch containing every message verbatim, plus compaction rewrites,
// shadow-git checkpoint markers, and the final stop reason.
//
// Write discipline: each entry is one marshaled line, appended and
// fsynced under a mutex. A crash mid-write leaves at most one torn
// trailing line, which LoadTranscript tolerates by dropping it. All
// writer methods are nil-receiver safe and never fail the run
// (fail-open, matching the worker-log contract).
package engine

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
)

// EnvDisableTranscript is the kill switch: when set to "1" the native
// runner skips transcript writing even if RunSpec.TranscriptPath is set.
const EnvDisableTranscript = "R1_DISABLE_TRANSCRIPT"

// Transcript entry type discriminators.
const (
	transcriptTypeMeta       = "meta"
	transcriptTypeMessage    = "message"
	transcriptTypeCompaction = "compaction"
	transcriptTypeCheckpoint = "checkpoint"
	transcriptTypeEnd        = "end"
)

// CheckpointRecord ties a shadow-git checkpoint (a dangling commit
// capturing the full working tree after a mutating tool call) to its
// position in the transcript's event stream. SHA is authoritative; Ref
// only pins the commit against GC and may be deleted by worktree
// cleanup without invalidating a transcript that recorded the SHA.
type CheckpointRecord struct {
	Seq  int    `json:"seq"`
	SHA  string `json:"sha"`
	Ref  string `json:"ref,omitempty"`
	Tool string `json:"tool,omitempty"`
}

// TranscriptEntry is one line of the JSONL transcript.
//
//   - "meta":       run header (model, phase, prompt hashes, correlation IDs).
//     Also a dispatch boundary: one file may hold several dispatches
//     (retry-with-rewind reuses the worktree, so the path repeats) and
//     replay resets at each meta so LoadTranscript yields the LAST
//     dispatch's conversation.
//   - "message":    one conversation message, verbatim, at history index Idx
//   - "compaction": the FULL rewritten history after a CompactFn pass —
//     replaces every message recorded before it
//   - "checkpoint": shadow-git checkpoint marker (see CheckpointRecord)
//   - "end":        loop terminated with StopReason Stop
type TranscriptEntry struct {
	Type       string              `json:"type"`
	TS         string              `json:"ts"`
	Turn       int                 `json:"turn,omitempty"`
	Idx        int                 `json:"idx,omitempty"`
	Message    *agentloop.Message  `json:"message,omitempty"`
	Messages   []agentloop.Message `json:"messages,omitempty"`
	Checkpoint *CheckpointRecord   `json:"checkpoint,omitempty"`
	Stop       string              `json:"stop,omitempty"`
	Meta       map[string]any      `json:"meta,omitempty"`
}

// transcriptWriter appends TranscriptEntry lines to a single file.
// Safe for concurrent use; all methods are nil-receiver safe so call
// sites don't need to guard every append behind "if tw != nil".
type transcriptWriter struct {
	mu      sync.Mutex
	f       *os.File
	written int // messages already persisted from the current history
}

// newTranscriptWriter opens (append/create) the transcript file. The
// parent directory must exist — mirroring the WorkerLogPath contract.
func newTranscriptWriter(path string) (*transcriptWriter, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &transcriptWriter{f: f}, nil
}

// writeEntryLocked marshals and appends one line, then fsyncs so a
// process crash can tear at most the line currently being written.
// Callers must hold w.mu.
func (w *transcriptWriter) writeEntryLocked(e TranscriptEntry) {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return // fail-open: a bad entry never fails the run
	}
	if _, err := w.f.Write(append(b, '\n')); err != nil {
		return
	}
	_ = w.f.Sync()
}

// meta writes the run header entry.
func (w *transcriptWriter) meta(m map[string]any) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeEntryLocked(TranscriptEntry{Type: transcriptTypeMeta, Meta: m})
}

// appendNew persists any messages not yet written. The agentloop's
// PreTurnHook hands the full history each turn; only the suffix past
// the internal high-water mark is appended, so the file stays
// append-only and each message lands exactly once.
func (w *transcriptWriter) appendNew(messages []agentloop.Message, turn int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(messages) < w.written {
		// History shrank without a compaction record (should not
		// happen — compaction() is the only sanctioned rewrite).
		// Resync with a full-history compaction entry so replay
		// stays correct rather than silently diverging.
		w.writeEntryLocked(TranscriptEntry{
			Type:     transcriptTypeCompaction,
			Turn:     turn,
			Messages: messages,
		})
		w.written = len(messages)
		return
	}
	for i := w.written; i < len(messages); i++ {
		msg := messages[i]
		w.writeEntryLocked(TranscriptEntry{
			Type:    transcriptTypeMessage,
			Turn:    turn,
			Idx:     i,
			Message: &msg,
		})
	}
	w.written = len(messages)
}

// compaction records a CompactFn rewrite: the full new history replaces
// everything recorded before this entry.
func (w *transcriptWriter) compaction(messages []agentloop.Message) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeEntryLocked(TranscriptEntry{
		Type:     transcriptTypeCompaction,
		Messages: messages,
	})
	w.written = len(messages)
}

// checkpoint records a shadow-git checkpoint marker.
func (w *transcriptWriter) checkpoint(rec CheckpointRecord) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeEntryLocked(TranscriptEntry{Type: transcriptTypeCheckpoint, Checkpoint: &rec})
}

// end records the loop's stop reason.
func (w *transcriptWriter) end(stop string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeEntryLocked(TranscriptEntry{Type: transcriptTypeEnd, Stop: stop})
}

// Close releases the underlying file.
func (w *transcriptWriter) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.f.Close()
}

// readTranscriptEntries parses every complete line of a transcript. A
// torn trailing line (crash mid-append) is dropped; a malformed line
// anywhere else is a hard error (the file has been corrupted, and a
// replay from corrupt state must fail closed).
func readTranscriptEntries(path string) ([]TranscriptEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	// Messages carry entire file bodies as tool results; size the line
	// buffer accordingly (mirrors the bus WAL's oversized scanner).
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("transcript %s: %w", path, err)
	}

	entries := make([]TranscriptEntry, 0, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e TranscriptEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			if i == len(lines)-1 {
				break // torn trailing line from a crash — tolerated
			}
			return nil, fmt.Errorf("transcript %s line %d: %w", path, i+1, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// replayTranscriptEntries folds entries into the message history they
// describe: message entries append, compaction entries replace, meta
// entries reset (dispatch boundary — only the last dispatch's history
// survives), and the remaining types are markers with no history effect.
func replayTranscriptEntries(entries []TranscriptEntry) []agentloop.Message {
	var msgs []agentloop.Message
	for _, e := range entries {
		switch e.Type {
		case transcriptTypeMeta:
			msgs = nil
		case transcriptTypeMessage:
			if e.Message != nil {
				msgs = append(msgs, *e.Message)
			}
		case transcriptTypeCompaction:
			msgs = append([]agentloop.Message(nil), e.Messages...)
		}
	}
	return msgs
}

// LoadTranscript replays a transcript file into the conversation
// history it recorded. Fails closed when tool_use/tool_result pairing
// is broken (the Messages API rejects dangling tool_use ids, so a
// caller must fall back to a fresh run rather than resume).
//
// Known caveat for resume (follow-up work, not this layer): extended-
// thinking signatures are not preserved by the loop (agentloop drops
// them when converting responses), so transcripts of ThinkingBudget>0
// runs are lossy. The native runner never sets ThinkingBudget today.
func LoadTranscript(path string) ([]agentloop.Message, error) {
	entries, err := readTranscriptEntries(path)
	if err != nil {
		return nil, err
	}
	msgs := replayTranscriptEntries(entries)
	if err := validateToolPairing(msgs); err != nil {
		return nil, fmt.Errorf("transcript %s: %w", path, err)
	}
	return msgs, nil
}

// validateToolPairing enforces the Messages API contract: every
// tool_use id in an assistant message must be answered by a
// tool_result in the immediately following message.
func validateToolPairing(msgs []agentloop.Message) error {
	for i, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		var uses []string
		for _, b := range m.Content {
			if b.Type == "tool_use" {
				uses = append(uses, b.ID)
			}
		}
		if len(uses) == 0 {
			continue
		}
		if i+1 >= len(msgs) {
			return fmt.Errorf("dangling tool_use %q: assistant message %d has no following tool_result message", uses[0], i)
		}
		answered := map[string]bool{}
		for _, b := range msgs[i+1].Content {
			if b.Type == "tool_result" {
				answered[b.ToolUseID] = true
			}
		}
		for _, id := range uses {
			if !answered[id] {
				return fmt.Errorf("dangling tool_use %q at message %d: no matching tool_result", id, i)
			}
		}
	}
	return nil
}

// TruncateTranscriptToCheckpoint rewinds a transcript to the checkpoint
// entry with the given seq, dropping everything after it. Trailing
// messages that would leave a dangling tool_use (an assistant message
// whose tool_results were never recorded) are dropped too, so the kept
// history always replays to an API-valid conversation. The rewrite is
// atomic (tmp + rename); on any error the original file is untouched.
func TruncateTranscriptToCheckpoint(path string, seq int) error {
	entries, err := readTranscriptEntries(path)
	if err != nil {
		return err
	}
	// Search from the end: checkpoint seqs restart per dispatch, and a
	// multi-dispatch file should rewind within the LATEST dispatch.
	cut := -1
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Type == transcriptTypeCheckpoint && e.Checkpoint != nil && e.Checkpoint.Seq == seq {
			cut = i
			break
		}
	}
	if cut < 0 {
		return fmt.Errorf("transcript %s: checkpoint seq %d not found", path, seq)
	}
	kept := entries[:cut+1]

	// Pairing repair: peel trailing message entries until the replayed
	// history no longer ends in an assistant message with unanswered
	// tool_use blocks. Checkpoints are written between a tool handler
	// returning and its tool_result being recorded, so at most a few
	// trailing entries need to go.
	for {
		msgs := replayTranscriptEntries(kept)
		if len(msgs) == 0 || !endsWithDanglingToolUse(msgs) {
			break
		}
		last := -1
		for i := len(kept) - 1; i >= 0; i-- {
			if kept[i].Type == transcriptTypeMessage {
				last = i
				break
			}
		}
		if last < 0 {
			// The dangling message came from a compaction entry's
			// embedded history — rewriting inside a compaction record
			// is not supported; fail closed.
			return fmt.Errorf("transcript %s: cannot repair tool pairing at checkpoint seq %d", path, seq)
		}
		kept = append(kept[:last], kept[last+1:]...)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	for _, e := range kept {
		b, mErr := json.Marshal(e)
		if mErr != nil {
			f.Close()
			os.Remove(tmp)
			return mErr
		}
		if _, wErr := f.Write(append(b, '\n')); wErr != nil {
			f.Close()
			os.Remove(tmp)
			return wErr
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// endsWithDanglingToolUse reports whether the final message is an
// assistant turn containing tool_use blocks (which, being last, can
// have no tool_result answer).
func endsWithDanglingToolUse(msgs []agentloop.Message) bool {
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" {
		return false
	}
	for _, b := range last.Content {
		if b.Type == "tool_use" {
			return true
		}
	}
	return false
}

// transcriptMetaFor builds the transcript's run-header map. Prompt
// bodies are recorded as sha256 hashes, not text: the first user
// message in the stream already carries the prompt verbatim, and
// hashes keep the header line greppable and small.
func transcriptMetaFor(spec RunSpec, model string, wlc *WorkerLogContext) map[string]any {
	m := map[string]any{
		"model":             model,
		"phase":             spec.Phase.Name,
		"worktree_dir":      spec.WorktreeDir,
		"max_turns":         spec.Phase.MaxTurns,
		"system_prompt_sha": sha256Hex(spec.SystemPrompt),
		"user_prompt_sha":   sha256Hex(spec.Prompt),
	}
	addCtx(m, wlc)
	return m
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
