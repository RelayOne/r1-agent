package jsonrpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/stokerr"
)

// TestHubHandler_CortexNotes_EmptyForFreshSession asserts that a
// session whose Workspace has no Notes returns an empty list — not
// nil, not an error.
func TestHubHandler_CortexNotes_EmptyForFreshSession(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// Attach an empty Workspace to the session so the handler has
	// something to project from.
	sess, err := h.Hub.Get(resp.SessionID)
	if err != nil {
		t.Fatalf("hub.Get: %v", err)
	}
	durable, err := bus.New(t.TempDir())
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	sess.Workspace = cortex.NewWorkspace(hub.New(), durable)

	notesResp, err := h.DaemonCortexNotes(context.Background(), CortexNotesRequest{SessionID: resp.SessionID})
	if err != nil {
		t.Fatalf("cortex.notes: %v", err)
	}
	if len(notesResp.Notes) != 0 {
		t.Errorf("expected empty notes, got %d", len(notesResp.Notes))
	}
	if notesResp.Notes == nil {
		t.Errorf("expected non-nil empty slice, got nil")
	}
}

// TestHubHandler_CortexNotes_ProjectsAndLimits publishes Notes onto
// a session's Workspace and asserts the handler returns them in
// most-recent-tail order, honouring Limit.
func TestHubHandler_CortexNotes_ProjectsAndLimits(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	sess, err := h.Hub.Get(resp.SessionID)
	if err != nil {
		t.Fatalf("hub.Get: %v", err)
	}
	durable, err := bus.New(t.TempDir())
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	ws := cortex.NewWorkspace(hub.New(), durable)
	sess.Workspace = ws

	// Publish 5 notes.
	now := time.Now()
	for i := 0; i < 5; i++ {
		n := cortex.Note{
			LobeID:    "test-lobe",
			Severity:  cortex.SevInfo,
			Title:     []string{"first", "second", "third", "fourth", "fifth"}[i],
			Body:      "body text",
			EmittedAt: now.Add(time.Duration(i) * time.Millisecond),
		}
		if err := ws.Publish(n); err != nil {
			t.Fatalf("publish #%d: %v", i, err)
		}
	}

	// Limit=0 returns all 5.
	all, err := h.DaemonCortexNotes(context.Background(), CortexNotesRequest{SessionID: resp.SessionID})
	if err != nil {
		t.Fatalf("cortex.notes (limit=0): %v", err)
	}
	if len(all.Notes) != 5 {
		t.Errorf("limit=0: expected 5 notes, got %d", len(all.Notes))
	}
	if len(all.Notes) > 0 && all.Notes[0].Title != "first" {
		t.Errorf("limit=0: expected oldest-first, got %q first", all.Notes[0].Title)
	}

	// Limit=3 returns the LAST 3 (most recent).
	last3, err := h.DaemonCortexNotes(context.Background(), CortexNotesRequest{SessionID: resp.SessionID, Limit: 3})
	if err != nil {
		t.Fatalf("cortex.notes (limit=3): %v", err)
	}
	if len(last3.Notes) != 3 {
		t.Errorf("limit=3: expected 3 notes, got %d", len(last3.Notes))
	}
	if len(last3.Notes) >= 3 {
		if last3.Notes[0].Title != "third" {
			t.Errorf("limit=3: expected first-of-tail = 'third', got %q", last3.Notes[0].Title)
		}
		if last3.Notes[2].Title != "fifth" {
			t.Errorf("limit=3: expected last = 'fifth', got %q", last3.Notes[2].Title)
		}
	}

	// Severity is projected.
	if len(last3.Notes) > 0 && last3.Notes[0].Severity != "info" {
		t.Errorf("expected severity=info; got %q", last3.Notes[0].Severity)
	}
	// At is RFC3339Nano.
	if len(last3.Notes) > 0 {
		if _, err := time.Parse(time.RFC3339Nano, last3.Notes[0].At); err != nil {
			t.Errorf("At not RFC3339Nano: %v (%q)", err, last3.Notes[0].At)
		}
	}
}

// TestHubHandler_CortexNotes_NoWorkspace returns an empty list when
// the session has no Workspace attached.
func TestHubHandler_CortexNotes_NoWorkspace(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	notesResp, err := h.DaemonCortexNotes(context.Background(), CortexNotesRequest{SessionID: resp.SessionID})
	if err != nil {
		t.Fatalf("cortex.notes (no workspace): %v", err)
	}
	if len(notesResp.Notes) != 0 {
		t.Errorf("expected empty notes when Workspace=nil, got %d", len(notesResp.Notes))
	}
}

// TestHubHandler_CortexNotes_RejectsMissingSessionID exercises the
// SessionIDRequest validation gate.
func TestHubHandler_CortexNotes_RejectsMissingSessionID(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	_, err := h.DaemonCortexNotes(context.Background(), CortexNotesRequest{SessionID: ""})
	if err == nil {
		t.Fatal("expected error for empty session_id")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrValidation {
		t.Errorf("expected ErrValidation, got %v", err)
	}

	_, err = h.DaemonCortexNotes(context.Background(), CortexNotesRequest{SessionID: "s-unknown"})
	if err == nil {
		t.Fatal("expected error for unknown session_id")
	}
	if !errors.As(err, &se) || se.Code != stokerr.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
