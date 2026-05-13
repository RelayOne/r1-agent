// audit.go — fire-and-forget audit egress for `r1 --one-shot`.
// Every invocation emits exactly one AuditEnvelope to a remote
// HTTP endpoint (the RelayGate audit pipeline). The submission
// path is non-blocking: a full queue or a slow endpoint never
// stalls the process exit.
//
// HMAC-SHA256 signing protects the integrity of the envelope in
// transit. The RelayGate audit service verifies the signature
// using the same shared secret (passed in via --audit-token or
// the R1_AUDIT_TOKEN env var).
//
// Retry policy: each envelope gets 3 retries with exponential
// backoff (200ms → 800ms → 3.2s). The HTTP client has a
// per-attempt 5 s timeout. Final failure logs an
// oneshot.audit.failed envelope to stderr; the verb's exit code
// is unchanged so audit is a strict telemetry channel.
//
// Spec: specs/oneshot-production-hardening.md §T4.2 / §T4.3.
package oneshot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// AuditEnvelope is the canonical body POSTed to the remote
// audit endpoint. SchemaVersion is the AuditSchemaVersion
// constant; EmittedAt is RFC3339 nanosecond precision UTC.
// All struct tags are stable wire identifiers — bump
// AuditSchemaVersion when this shape changes.
type AuditEnvelope struct {
	Verb            string  `json:"verb"`
	Status          string  `json:"status"`
	DurationMs      int64   `json:"duration_ms"`
	CostEstimateUSD float64 `json:"cost_estimate_usd"`
	PayloadSHA256   string  `json:"payload_sha256"`
	ResponseSHA256  string  `json:"response_sha256"`
	CorrelationID   string  `json:"correlation_id"`
	SchemaVersion   string  `json:"schema_version"`
	EmittedAt       string  `json:"emitted_at"`
}

// auditQueueCapacity caps the in-flight envelope buffer. 64 is
// large enough to absorb a brief endpoint stall without
// dropping; small enough that a wedged endpoint surfaces
// quickly via oneshot.audit.dropped. Bumping this is cheap if
// operator traffic spikes.
const auditQueueCapacity = 64

// auditRetrySchedule controls the per-envelope retry backoffs.
// First attempt is immediate (no delay); subsequent attempts
// wait the listed duration before retrying. Total worst case
// per envelope: 200ms + 800ms + 3.2s = 4.2 s wall clock plus
// up to four 5 s HTTP timeouts.
var auditRetrySchedule = []time.Duration{
	0,
	200 * time.Millisecond,
	800 * time.Millisecond,
	3200 * time.Millisecond,
}

// AuditClient is the per-invocation audit submitter. Constructed
// once in runOneShotCmd via NewAuditClient and torn down via
// DrainOrDrop at exit. Safe for concurrent Submit calls.
type AuditClient struct {
	Endpoint string
	Token    []byte
	HTTP     *http.Client

	// Stderr is the sink for oneshot.audit.failed envelopes.
	// Defaults to os.Stderr; tests override via the dedicated
	// constructor variant in audit_test.go.
	Stderr io.Writer

	queue   chan AuditEnvelope
	dropped atomic.Int64
	sent    atomic.Int64
	wg      sync.WaitGroup

	// stopCh closes when the worker should drop in-flight
	// retries and exit immediately. Closed by DrainOrDrop on
	// ctx cancel.
	stopCh chan struct{}
	stopMu sync.Mutex
	stoped bool
}

// NewAuditClient constructs an AuditClient and immediately
// spawns its worker goroutine. The worker consumes envelopes
// from the queue until Close is called. Pre-condition: caller
// has already validated endpoint (https or http-loopback) and
// has a non-empty token (the CLI's flag parser enforces both).
func NewAuditClient(endpoint, token string) *AuditClient {
	a := &AuditClient{
		Endpoint: endpoint,
		Token:    []byte(token),
		HTTP:     &http.Client{Timeout: 5 * time.Second},
		Stderr:   os.Stderr,
		queue:    make(chan AuditEnvelope, auditQueueCapacity),
		stopCh:   make(chan struct{}),
	}
	a.wg.Add(1)
	go a.worker()
	return a
}

// Submit queues an envelope for asynchronous POST. Never blocks:
// a full queue drops the envelope and bumps the dropped counter.
// Drops are surfaced at exit via the oneshot.audit.dropped
// envelope written by DrainOrDrop's caller.
func (a *AuditClient) Submit(env AuditEnvelope) {
	if a == nil {
		return
	}
	select {
	case a.queue <- env:
	default:
		a.dropped.Add(1)
	}
}

// DrainOrDrop closes the queue, waits for the worker to finish
// any in-flight envelopes (subject to ctx), and reports how many
// envelopes were successfully sent vs dropped. On ctx
// cancellation it returns immediately; remaining in-queue
// envelopes are counted as dropped.
//
// Returns (sent, dropped). Safe to call exactly once per
// AuditClient.
func (a *AuditClient) DrainOrDrop(ctx context.Context) (int, int) {
	if a == nil {
		return 0, 0
	}
	a.stopMu.Lock()
	if !a.stoped {
		a.stoped = true
		close(a.queue)
	}
	a.stopMu.Unlock()

	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Worker drained cleanly.
	case <-ctx.Done():
		// Force the worker to bail on its current retry cycle.
		a.stopMu.Lock()
		select {
		case <-a.stopCh:
			// already closed
		default:
			close(a.stopCh)
		}
		a.stopMu.Unlock()
		<-done
	}

	sent := int(a.sent.Load())
	dropped := int(a.dropped.Load())
	return sent, dropped
}

// worker is the single consumer goroutine. Runs each envelope
// through postWithRetry; bumps sent or logs failure.
func (a *AuditClient) worker() {
	defer a.wg.Done()
	for env := range a.queue {
		select {
		case <-a.stopCh:
			a.dropped.Add(1)
			// Drain remaining queued envelopes as dropped so
			// the final counts at DrainOrDrop return are
			// accurate. The range loop above will continue to
			// pull from the closed queue until it's empty.
			for range a.queue {
				a.dropped.Add(1)
			}
			return
		default:
		}
		if a.postWithRetry(env) {
			a.sent.Add(1)
		} else {
			a.dropped.Add(1)
		}
	}
}

// postWithRetry runs the retry schedule against the supplied
// envelope. Returns true on a 2xx response; false after all
// retries exhausted (logs oneshot.audit.failed in that case).
//
// Each attempt inherits the HTTP client's 5 s timeout AND a
// per-request context tied to a.stopCh so DrainOrDrop can yank
// a wedged in-flight POST without waiting for the HTTP client's
// own timeout to fire.
func (a *AuditClient) postWithRetry(env AuditEnvelope) bool {
	body, err := json.Marshal(env)
	if err != nil {
		fmt.Fprintf(a.Stderr, `{"event":%q,"correlation_id":%q,"verb":%q,"attempts":0,"last_status":0,"marshal_error":%q}`+"\n",
			EventAuditFailed, env.CorrelationID, env.Verb, err.Error())
		return false
	}
	mac := hmac.New(sha256.New, a.Token)
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	var lastStatus int
	for _, delay := range auditRetrySchedule {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-a.stopCh:
				return false
			}
		}
		// Per-attempt cancellable context: when stopCh closes
		// the cancel fires immediately, freeing the HTTP Do
		// call so DrainOrDrop returns within its own ctx.
		attemptCtx, attemptCancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-a.stopCh:
				attemptCancel()
			case <-attemptCtx.Done():
			}
		}()
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, a.Endpoint, bytes.NewReader(body))
		if err != nil {
			attemptCancel()
			lastStatus = 0
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(AuditSigHeader, sig)
		req.Header.Set(AuditCorrelationIDHeader, env.CorrelationID)
		req.Header.Set(AuditSchemaVersionHeader, env.SchemaVersion)

		resp, err := a.HTTP.Do(req)
		if err != nil {
			attemptCancel()
			// stopCh-driven cancellation: bail entirely.
			select {
			case <-a.stopCh:
				return false
			default:
			}
			lastStatus = 0
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		attemptCancel()
		lastStatus = resp.StatusCode
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return true
		}
	}
	fmt.Fprintf(a.Stderr, `{"event":%q,"correlation_id":%q,"verb":%q,"attempts":%d,"last_status":%d}`+"\n",
		EventAuditFailed, env.CorrelationID, env.Verb, len(auditRetrySchedule), lastStatus)
	return false
}

// QueuedDropped exposes the in-process drop counter for tests
// that need to assert overflow behavior without going through
// DrainOrDrop.
func (a *AuditClient) QueuedDropped() int64 {
	if a == nil {
		return 0
	}
	return a.dropped.Load()
}
