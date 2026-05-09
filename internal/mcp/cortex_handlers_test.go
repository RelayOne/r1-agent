package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// fakeCortex is a minimal CortexBackend for handler tests. The
// workspace is a real *cortex.Workspace (in-memory mode) so the
// notes/publish handlers exercise the real Note shape.
type fakeCortex struct {
	sessionID string
	ws        *cortex.Workspace
	infos     []cortex.LobeInfo
	pauseErr  error
}

func newFakeCortex(t *testing.T, sessionID string) *fakeCortex {
	t.Helper()
	return &fakeCortex{
		sessionID: sessionID,
		ws:        cortex.NewWorkspace(hub.New(), nil),
		infos: []cortex.LobeInfo{
			{ID: "memory-recall", Description: "recall", Kind: cortex.KindLLM, Paused: false},
			{ID: "rule-check", Description: "rules", Kind: cortex.KindDeterministic, Paused: true},
		},
	}
}

func (f *fakeCortex) SessionID() string             { return f.sessionID }
func (f *fakeCortex) Workspace() *cortex.Workspace  { return f.ws }
func (f *fakeCortex) LobeStatus() []cortex.LobeInfo { return f.infos }
func (f *fakeCortex) PauseLobe(id string) error {
	if f.pauseErr != nil {
		return f.pauseErr
	}
	for i := range f.infos {
		if f.infos[i].ID == id {
			f.infos[i].Paused = true
			return nil
		}
	}
	return errors.New("no Lobe with id " + id)
}
func (f *fakeCortex) ResumeLobe(id string) error {
	for i := range f.infos {
		if f.infos[i].ID == id {
			f.infos[i].Paused = false
			return nil
		}
	}
	return errors.New("no Lobe with id " + id)
}

func newServerWithCortex(t *testing.T, fc *fakeCortex) *StokeServer {
	t.Helper()
	s := NewStokeServer("/bin/true")
	s.WithCortex(fc)
	return s
}

func TestHandleCortexNotes_NoBackend(t *testing.T) {
	s := NewStokeServer("/bin/true")
	_, err := s.HandleToolCall("r1.cortex.notes", map[string]any{"session_id": "s1"})
	if err == nil || !strings.Contains(err.Error(), "cortex backend not wired") {
		t.Errorf("expected 'cortex backend not wired', got %v", err)
	}
}

func TestHandleCortexNotes_RoundTrip(t *testing.T) {
	fc := newFakeCortex(t, "s1")
	s := newServerWithCortex(t, fc)

	// Publish a few Notes.
	if err := fc.ws.Publish(cortex.Note{LobeID: "external", Severity: cortex.SevInfo, Title: "first"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := fc.ws.Publish(cortex.Note{LobeID: "external", Severity: cortex.SevCritical, Title: "second"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Notes — full snapshot.
	out, err := s.HandleToolCall("r1.cortex.notes", map[string]any{"session_id": "s1"})
	if err != nil {
		t.Fatalf("notes: %v", err)
	}
	var resp struct {
		Notes []map[string]any `json:"notes"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Count != 2 {
		t.Errorf("count = %d, want 2", resp.Count)
	}
	if got := resp.Notes[0]["title"]; got != "first" {
		t.Errorf("notes[0].title = %v, want 'first'", got)
	}
}

func TestHandleCortexNotes_SessionMismatch(t *testing.T) {
	fc := newFakeCortex(t, "real-session")
	s := newServerWithCortex(t, fc)
	_, err := s.HandleToolCall("r1.cortex.notes", map[string]any{"session_id": "different-session"})
	if err == nil || !strings.Contains(err.Error(), "session_id mismatch") {
		t.Errorf("expected mismatch error, got %v", err)
	}
}

func TestHandleCortexPublish_AdvisoryDefault(t *testing.T) {
	fc := newFakeCortex(t, "s1")
	s := newServerWithCortex(t, fc)
	out, err := s.HandleToolCall("r1.cortex.publish", map[string]any{
		"session_id": "s1",
		"note": map[string]any{
			"text": "hello world",
		},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !strings.Contains(out, `"severity":"advice"`) {
		t.Errorf("severity should default to advice; got %s", out)
	}
	notes := fc.ws.Snapshot()
	if len(notes) != 1 {
		t.Fatalf("workspace has %d notes, want 1", len(notes))
	}
	if notes[0].LobeID != "external" {
		t.Errorf("LobeID = %q, want external", notes[0].LobeID)
	}
}

func TestHandleCortexPublish_CriticalFlag(t *testing.T) {
	fc := newFakeCortex(t, "s1")
	s := newServerWithCortex(t, fc)
	_, err := s.HandleToolCall("r1.cortex.publish", map[string]any{
		"session_id": "s1",
		"note": map[string]any{
			"text":     "stop now",
			"critical": true,
			"tags":     []any{"manual-injection"},
		},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	notes := fc.ws.Snapshot()
	if notes[0].Severity != cortex.SevCritical {
		t.Errorf("Severity = %q, want critical", notes[0].Severity)
	}
}

func TestHandleCortexLobesList(t *testing.T) {
	fc := newFakeCortex(t, "s1")
	s := newServerWithCortex(t, fc)
	out, err := s.HandleToolCall("r1.cortex.lobes_list", map[string]any{"session_id": "s1"})
	if err != nil {
		t.Fatalf("lobes_list: %v", err)
	}
	var resp struct {
		Lobes []map[string]any `json:"lobes"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Count != 2 {
		t.Errorf("count = %d, want 2", resp.Count)
	}
	if resp.Lobes[1]["id"] != "rule-check" {
		t.Errorf("lobes[1].id = %v, want rule-check", resp.Lobes[1]["id"])
	}
	if resp.Lobes[1]["paused"] != true {
		t.Errorf("rule-check paused = %v, want true", resp.Lobes[1]["paused"])
	}
}

func TestHandleCortexLobePause_RoundTrip(t *testing.T) {
	fc := newFakeCortex(t, "s1")
	s := newServerWithCortex(t, fc)

	// Pause memory-recall (which starts unpaused).
	out, err := s.HandleToolCall("r1.cortex.lobe_pause", map[string]any{
		"session_id": "s1",
		"lobe":       "memory-recall",
	})
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if !strings.Contains(out, `"paused":true`) {
		t.Errorf("expected paused:true, got %s", out)
	}
	if !fc.infos[0].Paused {
		t.Error("fake state not updated")
	}

	// Resume it.
	out, err = s.HandleToolCall("r1.cortex.lobe_resume", map[string]any{
		"session_id": "s1",
		"lobe":       "memory-recall",
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !strings.Contains(out, `"paused":false`) {
		t.Errorf("expected paused:false, got %s", out)
	}

	// Unknown lobe → error propagates.
	_, err = s.HandleToolCall("r1.cortex.lobe_pause", map[string]any{
		"session_id": "s1",
		"lobe":       "does-not-exist",
	})
	if err == nil {
		t.Error("expected error for unknown lobe")
	}
}

func TestHandleCortexUnknownTool(t *testing.T) {
	fc := newFakeCortex(t, "s1")
	s := newServerWithCortex(t, fc)
	_, err := s.HandleToolCall("r1.cortex.does_not_exist", map[string]any{"session_id": "s1"})
	if err == nil || !strings.Contains(err.Error(), "unknown cortex tool") {
		t.Errorf("expected unknown-tool error, got %v", err)
	}
}
