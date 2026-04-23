package sessionctl

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func uniqueSessionID(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test-%d-%d", os.Getpid(), time.Now().UnixNano())
}

func TestServerStatusHappyPath(t *testing.T) {
	dir := t.TempDir()
	sid := uniqueSessionID(t)
	srv, err := StartServer(Opts{
		SocketDir: dir,
		SessionID: sid,
		Handlers: map[string]Handler{
			VerbStatus: func(req Request) (json.RawMessage, string, string) {
				return json.RawMessage(`{"state":"executing"}`), "", "evt-1"
			},
		},
	})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	defer srv.Close()

	resp, err := Call(srv.SocketPath(), Request{
		Verb:      VerbStatus,
		RequestID: "rq-1",
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !resp.OK {
		t.Fatalf("OK=false, Error=%q", resp.Error)
	}
	if !strings.Contains(string(resp.Data), "executing") {
		t.Errorf("Data missing \"executing\": %s", resp.Data)
	}
	if resp.RequestID != "rq-1" {
		t.Errorf("RequestID: got %q want %q", resp.RequestID, "rq-1")
	}
	if resp.EventID != "evt-1" {
		t.Errorf("EventID: got %q want %q", resp.EventID, "evt-1")
	}
}

func TestServerUnknownVerb(t *testing.T) {
	dir := t.TempDir()
	srv, err := StartServer(Opts{
		SocketDir: dir,
		SessionID: uniqueSessionID(t),
		Handlers:  map[string]Handler{},
	})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	defer srv.Close()

	resp, err := Call(srv.SocketPath(), Request{Verb: "foo", RequestID: "rq-2"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.OK {
		t.Fatalf("expected OK=false for unknown verb, got true")
	}
	if !strings.Contains(resp.Error, "unknown verb") || !strings.Contains(resp.Error, "foo") {
		t.Errorf("expected error to mention unknown verb foo, got %q", resp.Error)
	}
}

func TestServerCloseRemovesSocket(t *testing.T) {
	dir := t.TempDir()
	sid := uniqueSessionID(t)
	srv, err := StartServer(Opts{
		SocketDir: dir,
		SessionID: sid,
		Handlers:  map[string]Handler{VerbStatus: func(Request) (json.RawMessage, string, string) { return nil, "", "" }},
	})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	path := srv.SocketPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket should exist before Close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket should be gone after Close, stat err=%v", err)
	}
	// Second Close is a no-op.
	if err := srv.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestServerRequiresSessionID(t *testing.T) {
	_, err := StartServer(Opts{SocketDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected error when SessionID is empty")
	}
}

func TestServerHandlerError(t *testing.T) {
	dir := t.TempDir()
	srv, err := StartServer(Opts{
		SocketDir: dir,
		SessionID: uniqueSessionID(t),
		Handlers: map[string]Handler{
			VerbPause: func(Request) (json.RawMessage, string, string) {
				return nil, "not paused: no pgid", ""
			},
		},
	})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	defer srv.Close()

	resp, err := Call(srv.SocketPath(), Request{Verb: VerbPause, RequestID: "rq-3"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.OK {
		t.Errorf("expected OK=false when handler returns error")
	}
	if resp.Error != "not paused: no pgid" {
		t.Errorf("Error: got %q want %q", resp.Error, "not paused: no pgid")
	}
}
