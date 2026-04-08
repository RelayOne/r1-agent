package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthEndpoint(t *testing.T) {
	bus := NewEventBus()
	srv := New(0, "", bus)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status=ok, got %q", body["status"])
	}
}

func TestStatusEndpoint(t *testing.T) {
	bus := NewEventBus()
	srv := New(0, "", bus)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "running" {
		t.Fatalf("expected status=running, got %q", body["status"])
	}
}

func TestAuthRequired(t *testing.T) {
	bus := NewEventBus()
	srv := New(0, "secret-token", bus)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Request without auth header
	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthSuccess(t *testing.T) {
	bus := NewEventBus()
	srv := New(0, "secret-token", bus)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest("GET", ts.URL+"/api/status", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestStatusEndpointWithCustomFn(t *testing.T) {
	bus := NewEventBus()
	srv := New(0, "", bus)
	srv.StatusFn = func() interface{} {
		return map[string]interface{}{
			"phase":   "execute",
			"task_id": "TASK-42",
			"progress": 75,
		}
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["phase"] != "execute" {
		t.Errorf("expected phase=execute, got %v", body["phase"])
	}
	if body["task_id"] != "TASK-42" {
		t.Errorf("expected task_id=TASK-42, got %v", body["task_id"])
	}
	if body["progress"] != float64(75) {
		t.Errorf("expected progress=75, got %v", body["progress"])
	}
}

func TestSSEEventDelivery(t *testing.T) {
	bus := NewEventBus()
	srv := New(0, "", bus)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Create a context we can cancel to stop the SSE connection
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Start reading SSE in a goroutine
	type result struct {
		data string
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer resp.Body.Close()

		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		ch <- result{data: string(buf[:n])}
	}()

	// Give the SSE connection time to establish
	time.Sleep(50 * time.Millisecond)

	// Publish an event
	bus.Publish("hello-sse")

	// Read the result
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("SSE read error: %v", r.err)
		}
		if !strings.Contains(r.data, "data: hello-sse") {
			t.Errorf("SSE response should contain 'data: hello-sse', got: %q", r.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SSE event")
	}
	cancel()
}

func TestEventBusMultipleClients(t *testing.T) {
	bus := NewEventBus()
	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()

	bus.Publish("broadcast-msg")

	// Both subscribers should receive the message
	select {
	case msg := <-ch1:
		if msg != "broadcast-msg" {
			t.Errorf("ch1: expected 'broadcast-msg', got %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ch1: timed out waiting for event")
	}

	select {
	case msg := <-ch2:
		if msg != "broadcast-msg" {
			t.Errorf("ch2: expected 'broadcast-msg', got %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ch2: timed out waiting for event")
	}

	bus.Unsubscribe(ch1)
	bus.Unsubscribe(ch2)
}

func TestEventBusBufferFull(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()

	// Publish 100 events without reading -- should not deadlock
	// The channel buffer is 64, so events beyond that are dropped
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			bus.Publish("flood-event")
		}
		close(done)
	}()

	select {
	case <-done:
		// Success: no deadlock
	case <-time.After(5 * time.Second):
		t.Fatal("Publish deadlocked when buffer was full")
	}

	bus.Unsubscribe(ch)
}

func TestCORSHeaders(t *testing.T) {
	bus := NewEventBus()
	srv := New(0, "", bus)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	cors := resp.Header.Get("Access-Control-Allow-Origin")
	if cors != "*" {
		t.Errorf("expected Access-Control-Allow-Origin=*, got %q", cors)
	}
}

func TestEventBusPubSub(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()

	go func() {
		time.Sleep(10 * time.Millisecond)
		bus.Publish("hello")
	}()

	select {
	case msg := <-ch:
		if msg != "hello" {
			t.Fatalf("expected 'hello', got %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	bus.Unsubscribe(ch)
}
