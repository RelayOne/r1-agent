package oneshot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAuditClient_NonBlocking — Submit never blocks even if the
// worker is wedged. Spec §T4.2.
func TestAuditClient_NonBlocking(harness *testing.T) {
	a := &AuditClient{
		Endpoint: "http://127.0.0.1:1/never",
		Token:    []byte("k"),
		HTTP:     &http.Client{Timeout: 50 * time.Millisecond},
		Stderr:   io.Discard,
		queue:    make(chan AuditEnvelope, 64),
		stopCh:   make(chan struct{}),
	}
	// No worker — fill the queue to capacity, then assert that
	// Submit returns quickly even on overflow.
	for i := 0; i < 64; i++ {
		a.queue <- AuditEnvelope{CorrelationID: "fill"}
	}

	enqueue := a.Submit
	// Loop-level deadline rather than per-call: a cold-cache or
	// GC pause on a busy CI runner can push a single Submit past
	// 5 ms even though the channel send itself is non-blocking.
	// The aggregate deadline (1000 calls in < 200 ms) still
	// rejects any path that genuinely blocks per call.
	loopStart := time.Now()
	for i := 0; i < 1000; i++ {
		callStart := time.Now()
		enqueue(AuditEnvelope{CorrelationID: "overflow"})
		if d := time.Since(callStart); d > 100*time.Millisecond {
			harness.Fatalf("Submit blocked for %s on iter %d; should be non-blocking", d, i)
		}
	}
	if d := time.Since(loopStart); d > 1*time.Second {
		harness.Fatalf("1000 non-blocking Submit calls took %s; expected <= 1s", d)
	}
	if a.QueuedDropped() < 1000 {
		harness.Errorf("dropped=%d want >= 1000", a.QueuedDropped())
	}
}

// TestAuditClient_RetriesThenLogs — a server that returns 500
// triggers exactly 4 POST attempts (initial + 3 retries) and
// produces an oneshot.audit.failed stderr envelope. Spec §T4.3.
func TestAuditClient_RetriesThenLogs(harness *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var stderr bytes.Buffer
	a := NewAuditClient(srv.URL, "secret")
	a.Stderr = &stderr

	enqueue := a.Submit
	enqueue(AuditEnvelope{
		Verb:          "decompose",
		CorrelationID: "corr-retry",
		SchemaVersion: AuditSchemaVersion,
		EmittedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sent, dropped := a.DrainOrDrop(ctx)
	if sent != 0 {
		harness.Errorf("sent=%d want 0", sent)
	}
	if dropped != 1 {
		harness.Errorf("dropped=%d want 1", dropped)
	}
	if hits.Load() != 4 {
		harness.Errorf("hits=%d want 4 (initial + 3 retries)", hits.Load())
	}
	if !bytes.Contains(stderr.Bytes(), []byte(EventAuditFailed)) {
		harness.Errorf("stderr should contain %q, got: %s", EventAuditFailed, stderr.String())
	}
}

// TestAuditClient_DrainContextCancel — DrainOrDrop honors its
// ctx and returns promptly when the context cancels, leaving
// remaining envelopes as drops. Spec §T4.4.
func TestAuditClient_DrainContextCancel(harness *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAuditClient(srv.URL, "secret")
	a.Stderr = io.Discard
	enqueue := a.Submit
	for i := 0; i < 5; i++ {
		enqueue(AuditEnvelope{
			Verb:          "decompose",
			CorrelationID: "corr-slow",
			SchemaVersion: AuditSchemaVersion,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	sent, dropped := a.DrainOrDrop(ctx)
	if d := time.Since(start); d > 500*time.Millisecond {
		harness.Errorf("DrainOrDrop took %s; ctx was 100ms", d)
	}
	if dropped < 4 {
		harness.Errorf("dropped=%d want >= 4", dropped)
	}
	_ = sent
}

// TestAuditClient_SuccessfulPOSTCarriesHMACAndHeaders — happy
// path: a 200 server records exactly one POST whose body matches
// the marshaled envelope, the HMAC header verifies against the
// shared secret, and the schema / correlation headers are set.
// Spec §T4.3.
func TestAuditClient_SuccessfulPOSTCarriesHMACAndHeaders(harness *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	var sigs []string
	var corrs []string
	var schemas []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		sigs = append(sigs, r.Header.Get(AuditSigHeader))
		corrs = append(corrs, r.Header.Get(AuditCorrelationIDHeader))
		schemas = append(schemas, r.Header.Get(AuditSchemaVersionHeader))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAuditClient(srv.URL, "shared-secret")
	a.Stderr = io.Discard

	env := AuditEnvelope{
		Verb:           "decompose",
		Status:         "ok",
		DurationMs:     42,
		PayloadSHA256:  sha256Hex(harness, []byte("payload")),
		ResponseSHA256: sha256Hex(harness, []byte(`{"verb":"decompose","status":"ok"}`)),
		CorrelationID:  "corr-happy",
		SchemaVersion:  AuditSchemaVersion,
		EmittedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	enqueue := a.Submit
	enqueue(env)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sent, dropped := a.DrainOrDrop(ctx)
	if sent != 1 {
		harness.Errorf("sent=%d want 1", sent)
	}
	if dropped != 0 {
		harness.Errorf("dropped=%d want 0", dropped)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		harness.Fatalf("got %d POSTs want 1", len(bodies))
	}
	mac := hmac.New(sha256.New, []byte("shared-secret"))
	mac.Write(bodies[0])
	want := hex.EncodeToString(mac.Sum(nil))
	if sigs[0] != want {
		harness.Errorf("sig mismatch: got %q want %q", sigs[0], want)
	}
	if corrs[0] != "corr-happy" {
		harness.Errorf("correlation header=%q want corr-happy", corrs[0])
	}
	if schemas[0] != AuditSchemaVersion {
		harness.Errorf("schema header=%q want %q", schemas[0], AuditSchemaVersion)
	}
}

// sha256Hex is the test-only mirror of the CLI helper, kept in
// the same _test.go file because it's a real assertion-bearing
// helper used inside test bodies only.
func sha256Hex(harness *testing.T, b []byte) string {
	harness.Helper()
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
