// cortex_server.go — MCP r1.cortex.* tool handlers.
//
// Spec 8 (agentic-test-harness) §4.3 + §12 specifies 5 tools that
// expose the cortex Workspace and Lobe lifecycle to external MCP
// clients (r1.cortex.notes, .publish, .lobes_list, .lobe_pause,
// .lobe_resume). This file ships the handlers; r1_server.go's
// HandleToolCall routes the r1.cortex.* prefix here.
//
// Wiring: the daemon (cmd/r1-server/) constructs both the MCP server
// and a *cortex.Cortex; once both exist, call s.WithCortex(c) to
// attach the cortex backend. Without that wiring, every cortex tool
// call returns "cortex backend not wired".
//
// Surfaced by audit/scan-go-stubs.md item #1.
//
// See:
//   - specs/cortex-core.md (Workspace + LobeRunner contract)
//   - specs/cortex-concerns.md (Lobe interface)
//   - specs/agentic-test-harness.md §4.3 (tool catalog)
package mcp

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
)

// CortexToolNames returns the canonical names of the 5 cortex tools per
// spec 8 §4.3. Used by the lint at tools/lint-view-without-api/ to verify
// the web Workspace pane and the TUI Workspace renderer reference each
// one.
func CortexToolNames() []string {
	return []string{
		"r1.cortex.notes",
		"r1.cortex.publish",
		"r1.cortex.lobes_list",
		"r1.cortex.lobe_pause",
		"r1.cortex.lobe_resume",
	}
}

// CortexBackend is the surface area the MCP cortex handlers need from a
// running *cortex.Cortex. Defining it as an interface keeps the MCP
// package independent of cortex internals and lets tests substitute a
// fake backend without spinning up a Cortex.
type CortexBackend interface {
	// SessionID returns the cortex's configured session id. Handlers
	// reject calls whose session_id arg does not match this value so
	// one cortex instance never serves another session's requests.
	SessionID() string

	// Workspace returns the cortex's Workspace handle. The notes
	// handler reads via Snapshot(); the publish handler writes via
	// Publish.
	Workspace() *cortex.Workspace

	// LobeStatus reports identity + pause state for every registered
	// Lobe.
	LobeStatus() []cortex.LobeInfo

	// PauseLobe / ResumeLobe toggle the runner pause flag. Both
	// return a non-nil error when the lobe id is not registered.
	PauseLobe(id string) error
	ResumeLobe(id string) error
}

// notesCap is the maximum number of Notes returned by a single
// r1.cortex.notes call. Caps protect the wire from a noisy session
// flooding an external client; callers can paginate via since_seq.
const notesCap = 200

// noteJSON is the wire-format projection of cortex.Note for the
// r1.cortex.notes / r1.cortex.publish tool results. We do not return
// the raw cortex.Note value because its time.Time field would render
// as Go's default RFC3339Nano format and Tags is omitempty in the
// underlying struct; this projection is stable across cortex internal
// refactors.
type noteJSON struct {
	ID        string         `json:"id"`
	LobeID    string         `json:"lobe_id"`
	Severity  string         `json:"severity"`
	Title     string         `json:"title"`
	Body      string         `json:"body,omitempty"`
	Tags      []string       `json:"tags,omitempty"`
	Round     uint64         `json:"round"`
	EmittedAt string         `json:"emitted_at"`
	Resolves  string         `json:"resolves,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

func projectNote(n cortex.Note) noteJSON {
	return noteJSON{
		ID:        n.ID,
		LobeID:    n.LobeID,
		Severity:  string(n.Severity),
		Title:     n.Title,
		Body:      n.Body,
		Tags:      n.Tags,
		Round:     n.Round,
		EmittedAt: n.EmittedAt.UTC().Format(time.RFC3339Nano),
		Resolves:  n.Resolves,
		Meta:      n.Meta,
	}
}

// requireCortexAndSession pulls the attached backend, validates the
// args have the required session_id, and returns the backend for the
// caller to use. Returns a structured error suitable for the MCP
// tools/call response when either gate fails.
func (s *StokeServer) requireCortexAndSession(args map[string]interface{}) (CortexBackend, error) {
	s.mu.Lock()
	c := s.cortex
	s.mu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("cortex backend not wired")
	}
	sid, _ := args["session_id"].(string)
	if sid == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if want := c.SessionID(); want != "" && sid != want {
		return nil, fmt.Errorf("session_id mismatch: this cortex instance serves session %q, not %q", want, sid)
	}
	return c, nil
}

// handleCortexNotes implements r1.cortex.notes. Returns the JSON-
// serialized list of Notes in the workspace whose Round > since_seq,
// capped at notesCap entries. The wire schema is documented in
// r1_server_catalog.go r1CortexTools().
func (s *StokeServer) handleCortexNotes(args map[string]interface{}) (string, error) {
	c, err := s.requireCortexAndSession(args)
	if err != nil {
		return "", err
	}
	since, _ := args["since_seq"].(float64) // JSON numbers arrive as float64
	ws := c.Workspace()
	if ws == nil {
		return "", fmt.Errorf("cortex workspace not initialized")
	}
	all := ws.Snapshot()
	out := make([]noteJSON, 0, len(all))
	// Filter rule: since_seq=0 (the default) returns every Note,
	// including unstamped (Round=0) Notes published outside a Round
	// context. since_seq>0 returns only Notes whose Round is strictly
	// greater (a polling client paginates by passing the highest Round
	// it has seen). Rounds in cortex are 1-indexed (see Round.Open) so
	// "give me everything" maps cleanly to since_seq=0.
	sinceU := uint64(0)
	if since > 0 {
		sinceU = uint64(since)
	}
	for _, n := range all {
		if sinceU > 0 && n.Round <= sinceU {
			continue
		}
		out = append(out, projectNote(n))
		if len(out) >= notesCap {
			break
		}
	}
	body, err := json.Marshal(map[string]any{
		"notes":  out,
		"count":  len(out),
		"capped": len(out) >= notesCap,
	})
	if err != nil {
		return "", fmt.Errorf("marshal notes: %w", err)
	}
	return string(body), nil
}

// handleCortexPublish implements r1.cortex.publish. Constructs a
// cortex.Note from args.note (text, tags, critical) with LobeID =
// "external" so it is distinguishable from Lobe-emitted Notes, then
// publishes via Workspace.Publish. Severity is SevCritical iff
// critical=true, else SevAdvice (external hints are advisory).
func (s *StokeServer) handleCortexPublish(args map[string]interface{}) (string, error) {
	c, err := s.requireCortexAndSession(args)
	if err != nil {
		return "", err
	}
	noteArg, _ := args["note"].(map[string]interface{})
	if noteArg == nil {
		return "", fmt.Errorf("note is required")
	}
	text, _ := noteArg["text"].(string)
	if text == "" {
		return "", fmt.Errorf("note.text is required")
	}
	critical, _ := noteArg["critical"].(bool)
	severity := cortex.SevAdvice
	if critical {
		severity = cortex.SevCritical
	}
	tags := tagsFromArgs(noteArg["tags"])
	// cortex.Note.Validate caps Title at 80 runes; truncate here so
	// the publish call does not return a validation error to the MCP
	// client when an external integrator passes a long text.
	title := truncateRunesForNote(text, 80)
	body := ""
	if len(text) > len(title) {
		body = text
	}
	n := cortex.Note{
		LobeID:    "external",
		Severity:  severity,
		Title:     title,
		Body:      body,
		Tags:      append([]string{"external"}, tags...),
		EmittedAt: time.Now(),
	}
	if err := c.Workspace().Publish(n); err != nil {
		return "", fmt.Errorf("publish note: %w", err)
	}
	resp, _ := json.Marshal(map[string]any{
		"ok":       true,
		"severity": string(n.Severity),
	})
	return string(resp), nil
}

// handleCortexLobesList implements r1.cortex.lobes_list. Returns the
// per-Lobe identity + pause state for every registered Lobe.
func (s *StokeServer) handleCortexLobesList(args map[string]interface{}) (string, error) {
	c, err := s.requireCortexAndSession(args)
	if err != nil {
		return "", err
	}
	infos := c.LobeStatus()
	out := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		out = append(out, map[string]any{
			"id":          info.ID,
			"description": info.Description,
			"kind":        kindString(info.Kind),
			"paused":      info.Paused,
		})
	}
	body, err := json.Marshal(map[string]any{
		"lobes": out,
		"count": len(out),
	})
	if err != nil {
		return "", fmt.Errorf("marshal lobes: %w", err)
	}
	return string(body), nil
}

// handleCortexLobePause implements r1.cortex.lobe_pause. Routes to
// CortexBackend.PauseLobe; returns the structured success or the
// wrapped not-found error.
func (s *StokeServer) handleCortexLobePause(args map[string]interface{}) (string, error) {
	c, err := s.requireCortexAndSession(args)
	if err != nil {
		return "", err
	}
	id, _ := args["lobe"].(string)
	if id == "" {
		return "", fmt.Errorf("lobe is required")
	}
	if err := c.PauseLobe(id); err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{"ok": true, "lobe": id, "paused": true})
	return string(body), nil
}

// handleCortexLobeResume implements r1.cortex.lobe_resume. Routes to
// CortexBackend.ResumeLobe; returns the structured success or the
// wrapped not-found error.
func (s *StokeServer) handleCortexLobeResume(args map[string]interface{}) (string, error) {
	c, err := s.requireCortexAndSession(args)
	if err != nil {
		return "", err
	}
	id, _ := args["lobe"].(string)
	if id == "" {
		return "", fmt.Errorf("lobe is required")
	}
	if err := c.ResumeLobe(id); err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{"ok": true, "lobe": id, "paused": false})
	return string(body), nil
}

func kindString(k cortex.LobeKind) string {
	switch k {
	case cortex.KindDeterministic:
		return "deterministic"
	case cortex.KindLLM:
		return "llm"
	default:
		return "unknown"
	}
}

func tagsFromArgs(v any) []string {
	raw, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		if s, ok := t.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func truncateRunesForNote(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
