// Curation pipeline for MemoryCuratorLobe (spec item 30).
//
// defaultOnTrigger is the production trigger callback installed by
// the constructor. It runs the spec-item-30 pipeline:
//
//  1. Privacy gate: if SkipPrivateMessages is set and any message in
//     the curatorRecentN tail carries the privateTagSentinel, skip the
//     entire haikuCall — the model is never shown private text.
//  2. Call haikuCall to obtain raw response Content blocks.
//  3. Walk the blocks for tool_use blocks named "remember"; parse each
//     block's input to a curatorCandidate.
//  4. Partition candidates by Category:
//       - Category in PrivacyConfig.AutoCurateCategories: auto-write
//         to memory.Store, append one AuditEntry to AuditLogPath, and
//         publish a confirmation Note (LobeID="memory-curator",
//         tag="memory").
//       - Otherwise: queue a confirm-Note (tag="memory-confirm",
//         Meta.action_kind="user-confirm") so the main thread can
//         surface it for explicit user approval.
//
// All errors are logged at warn level; the LobeRunner contract is
// "Run MUST return nil on graceful shutdown", so we never propagate
// per-candidate failures.
package memorycurator

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/provider"
)

// curatorCandidate captures one decoded "remember" tool call. Fields
// mirror the rememberTool input schema (see tool.go).
type curatorCandidate struct {
	Category memory.Category `json:"category"`
	Content  string          `json:"content"`
	Context  string          `json:"context,omitempty"`
	File     string          `json:"file,omitempty"`
	Tags     []string        `json:"tags,omitempty"`
	// SourceMsgID is the optional message-id the model attributed the
	// candidate to. The rememberTool schema does not currently expose
	// this field; it is reserved for future spec evolution and stays
	// empty in the current implementation. Recorded in the audit log
	// for forward compatibility.
	SourceMsgID string `json:"source_msg_id,omitempty"`
}

// defaultOnTrigger is the production trigger callback. See the package
// doc for the full pipeline contract.
func (l *MemoryCuratorLobe) defaultOnTrigger(ctx context.Context, in cortex.LobeInput) {
	// Privacy gate (spec TASK-30, "drop tool calls whose source-message-window
	// includes any message with tags:[\"private\"]"). When the gate
	// fires we skip the entire haikuCall — the model is never asked to
	// reason over private content.
	if l.privacy.SkipPrivateMessages {
		tail := tailMessages(in.History, curatorRecentN)
		if windowContainsPrivate(tail) {
			slog.Debug("memory-curator: privacy gate skipped haikuCall",
				"lobe", l.ID(),
				"tail_size", len(tail))
			return
		}
	}

	blocks := l.haikuCall(ctx, in)
	if len(blocks) == 0 {
		return
	}

	for _, blk := range blocks {
		if blk.Type != "tool_use" || blk.Name != rememberToolName {
			continue
		}
		cand, ok := decodeCandidate(blk)
		if !ok {
			slog.Warn("memory-curator: dropped malformed candidate",
				"lobe", l.ID(), "tool_use_id", blk.ID)
			continue
		}
		l.processCandidate(ctx, cand)
	}
}

// decodeCandidate extracts a curatorCandidate from a tool_use ResponseContent
// block. Returns ok=false on missing required fields (Category, Content)
// — those candidates are dropped silently per the LobeRunner "no
// per-candidate failures" rule.
func decodeCandidate(blk provider.ResponseContent) (curatorCandidate, bool) {
	cat, _ := blk.Input["category"].(string)
	content, _ := blk.Input["content"].(string)
	if cat == "" || content == "" {
		return curatorCandidate{}, false
	}
	contextText, _ := blk.Input["context"].(string)
	file, _ := blk.Input["file"].(string)

	var tags []string
	if raw, ok := blk.Input["tags"].([]any); ok {
		for _, t := range raw {
			if s, ok := t.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	return curatorCandidate{
		Category: memory.Category(cat),
		Content:  content,
		Context:  contextText,
		File:     file,
		Tags:     tags,
	}, true
}

// processCandidate is the per-candidate dispatcher: auto-apply if the
// candidate's category is in privacy.AutoCurateCategories; otherwise
// queue a confirm-Note. Both paths Publish a Note so the main thread
// has a record; only the auto-apply path writes to memory + audit.
func (l *MemoryCuratorLobe) processCandidate(ctx context.Context, cand curatorCandidate) {
	if err := ctx.Err(); err != nil {
		return
	}
	if l.isAutoCurate(cand.Category) {
		l.autoApply(cand)
		return
	}
	l.queueForConfirm(cand)
}

// isAutoCurate reports whether cat appears in privacy.AutoCurateCategories.
// Linear scan — the slice is fixed at startup and small (≤6 entries),
// so a map-set is unwarranted.
func (l *MemoryCuratorLobe) isAutoCurate(cat memory.Category) bool {
	for _, c := range l.privacy.AutoCurateCategories {
		if c == cat {
			return true
		}
	}
	return false
}

// autoApply persists cand to memory.Store, appends an AuditEntry to the
// configured audit log, and Publishes a confirmation Note. memory.Store
// failures (Save error) are logged but do not block the audit write —
// the audit log is the durable record of the decision.
func (l *MemoryCuratorLobe) autoApply(cand curatorCandidate) {
	var entryID string
	if l.mem != nil {
		entry := l.mem.RememberWithContext(cand.Category, cand.Content,
			cand.Context, cand.File, cand.Tags...)
		if entry != nil {
			entryID = entry.ID
		}
		// Save() is idempotent and best-effort; failures are logged but
		// the audit log still records the attempt.
		if err := l.mem.Save(); err != nil {
			slog.Warn("memory-curator: memory.Save failed",
				"err", err, "lobe", l.ID(), "category", cand.Category)
		}
	}

	ent := newAuditEntry(
		string(cand.Category),
		cand.Content,
		entryID,
		cand.SourceMsgID,
		"auto-applied",
	)
	if err := appendAuditLine(l.privacy.AuditLogPath, ent); err != nil {
		slog.Warn("memory-curator: audit append failed",
			"err", err, "lobe", l.ID(), "path", l.privacy.AuditLogPath)
	}

	l.publishMemoryNote(cand, entryID)
}

// queueForConfirm Publishes a confirm-Note for cand. The Note carries
// the candidate as a JSON blob in Meta["action_payload"] so the user-
// confirm handler (lifted into the main thread) can re-apply it when
// the user approves.
func (l *MemoryCuratorLobe) queueForConfirm(cand curatorCandidate) {
	if l.ws == nil {
		return
	}
	payload, err := json.Marshal(cand)
	if err != nil {
		slog.Warn("memory-curator: marshal candidate failed",
			"err", err, "lobe", l.ID())
		return
	}
	note := cortex.Note{
		LobeID:   l.ID(),
		Severity: cortex.SevInfo,
		Title:    "Remember this?",
		Body:     cand.Content,
		Tags:     []string{"memory-confirm"},
		Meta: map[string]any{
			llm.MetaActionKind:    "user-confirm",
			llm.MetaActionPayload: string(payload),
			"category":            string(cand.Category),
		},
	}
	if err := l.ws.Publish(note); err != nil {
		slog.Warn("memory-curator: publish confirm note failed",
			"err", err, "lobe", l.ID())
	}
}

// publishMemoryNote Publishes the auto-write confirmation Note that
// records "we just remembered X". Distinct from queueForConfirm — this
// Note is a passive announcement, not a user-action request.
func (l *MemoryCuratorLobe) publishMemoryNote(cand curatorCandidate, entryID string) {
	if l.ws == nil {
		return
	}
	title := "Remembered: " + truncateRunes(cand.Content, 60)
	note := cortex.Note{
		LobeID:   l.ID(),
		Severity: cortex.SevInfo,
		Title:    title,
		Body:     cand.Content,
		Tags:     []string{"memory"},
		Meta: map[string]any{
			"category": string(cand.Category),
			"entry_id": entryID,
		},
	}
	if err := l.ws.Publish(note); err != nil {
		slog.Warn("memory-curator: publish memory note failed",
			"err", err, "lobe", l.ID())
	}
}

// truncateRunes clips s so its rune count does not exceed n. cortex.Note
// Validate enforces a Title cap of 80 runes (not bytes); this helper
// counts runes and slices on rune boundaries so a multi-byte glyph at
// the boundary does not produce an invalid Title or trip Validate.
// Adds an ellipsis only when truncation happens.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 3 {
		return string(runes[:n])
	}
	return string(runes[:n-3]) + "..."
}
