package sessionctl

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/eventlog"
)

// TestIntegration_SocketRoundtrip_PersistsToEventlog spins up a real
// server + real eventlog DB, issues a mutating verb over the socket,
// and asserts the event landed in SQLite.
func TestIntegration_SocketRoundtrip_PersistsToEventlog(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "events.db")
	log, err := eventlog.Open(dbPath)
	if err != nil {
		t.Fatalf("eventlog.Open: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	const sessID = "ses_integ_1"

	// Real Emit: marshal the payload into bus.Event.Payload (json.RawMessage)
	// and Append it to the eventlog. bus.Event has no direct SessionID field;
	// log.Append mirrors Scope.LoopID into the session_id column, so set
	// Scope.LoopID to sessID so ReplaySession can find it.
	emit := func(kind string, payload any) string {
		raw, err := json.Marshal(payload)
		if err != nil {
			return ""
		}
		ev := bus.Event{
			Type:      bus.EventType(kind),
			EmitterID: "sessionctl-test",
			Scope:     bus.Scope{LoopID: sessID},
			Payload:   json.RawMessage(raw),
		}
		if err := log.Append(&ev); err != nil {
			return ""
		}
		return ev.ID
	}

	deps := Deps{
		SessionID: sessID,
		Router:    NewApprovalRouter(),
		Emit:      emit,
		InjectTask: func(text string, priority int) (string, error) {
			return "task_abc", nil
		},
	}

	srvSessID := "integ-" + time.Now().Format("150405.000000000")
	srv, err := StartServer(Opts{
		SocketDir: dir,
		SessionID: srvSessID,
		Handlers:  DefaultHandlers(deps),
	})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	sock := srv.SocketPath()
	payload, _ := json.Marshal(map[string]any{"text": "run tests again", "priority": 3})
	resp, err := Call(sock, Request{Verb: VerbInject, RequestID: "req1", Payload: payload})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resp.OK=false, error=%q", resp.Error)
	}
	if resp.EventID == "" {
		t.Fatalf("expected non-empty EventID")
	}

	// Verify the event landed in the log.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	found := false
	var foundID string
	for ev, readErr := range log.ReadFrom(ctx, 0) {
		if readErr != nil {
			t.Fatalf("ReadFrom: %v", readErr)
		}
		if string(ev.Type) == "operator.inject" {
			found = true
			foundID = ev.ID
			break
		}
	}
	if !found {
		t.Fatalf("operator.inject event not persisted")
	}
	if foundID != resp.EventID {
		t.Errorf("persisted event ID %q does not match response EventID %q", foundID, resp.EventID)
	}
}

// TestIntegration_DeadSocketPruned verifies that when the server
// exits, the socket file is removed and a fresh Call returns a
// "session ended" error.
func TestIntegration_DeadSocketPruned(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sessID := "integ-dead"
	srv, err := StartServer(Opts{
		SocketDir: dir,
		SessionID: sessID,
		Handlers:  DefaultHandlers(Deps{SessionID: sessID}),
	})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	sock := srv.SocketPath()

	// Close the server -- socket file should be removed.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// PruneStaleSocket should report false: the file is already gone, so
	// there is nothing to prune.
	if PruneStaleSocket(sock) {
		t.Errorf("PruneStaleSocket returned true for an already-gone socket")
	}

	// Call returns the expected error -- no live socket to dial.
	_, err = Call(sock, Request{Verb: VerbStatus, RequestID: "req1"})
	if err == nil {
		t.Fatalf("Call succeeded on closed socket; want error")
	}
}

// TestIntegration_DiscoveryRoundtrip seeds two live servers in the
// same ctlDir and confirms DiscoverSessions lists both.
func TestIntegration_DiscoveryRoundtrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a, err := StartServer(Opts{
		SocketDir: dir,
		SessionID: "a",
		Handlers:  DefaultHandlers(Deps{SessionID: "a"}),
	})
	if err != nil {
		t.Fatalf("StartServer a: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := StartServer(Opts{
		SocketDir: dir,
		SessionID: "b",
		Handlers:  DefaultHandlers(Deps{SessionID: "b"}),
	})
	if err != nil {
		t.Fatalf("StartServer b: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	socks, err := DiscoverSessions(dir)
	if err != nil {
		t.Fatalf("DiscoverSessions: %v", err)
	}
	if len(socks) != 2 {
		t.Fatalf("DiscoverSessions returned %d; want 2: %v", len(socks), socks)
	}
}
