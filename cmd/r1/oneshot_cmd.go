// CLI entry for `r1 --one-shot <verb> [flags...]`.
//
// Thin adapter over internal/oneshot. The A3 production
// hardening spec extends the original 3-verb adapter with
// six hardening concerns:
//
//   - T1 — bounded memory via --max-mem (RLIMIT_AS + GOMEMLIMIT)
//   - T2 — bounded wall-clock via --timeout with drop-partial
//   - T3 — deterministic shutdown on SIGINT / SIGTERM
//   - T4 — fire-and-forget audit POST to a remote endpoint
//   - T5 — 1000-concurrent integration test (separate file)
//   - T6 — operator runbook in docs/integrations/
//
// Spec: specs/oneshot-production-hardening.md.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/RelayOne/r1/internal/oneshot"
)

// Flag bounds — exported as constants so the doc-test can pin
// the runbook table against the code rather than maintaining
// the numbers in two places.
const (
	minMaxMemMiB = 32
	maxMaxMemMiB = 16384
	minTimeout   = 100 * time.Millisecond
	maxTimeout   = 30 * time.Minute
)

// runOneShotCmd is invoked from main when argv[1] is
// --one-shot / one-shot. Expects argv[2]=<verb> with optional
// flags. Returns the exit code; main calls os.Exit on the
// return value so the caller can substitute a test buffer.
//
// Exit codes are the integer constants in
// internal/oneshot/exit_codes.go:
//
//	ExitOK      (0)  — success, JSON response on stdout
//	ExitRuntime (1)  — internal failure (I/O, marshal, dispatch)
//	ExitUsage   (2)  — bad flag / unknown verb / range
//	ExitMemory  (3)  — hit RLIMIT_AS or GOMEMLIMIT ceiling
//	ExitTimeout (4)  — --timeout fired before verb completed
//	ExitSIGINT  (130)— SIGINT received
//	ExitSIGTERM (143)— SIGTERM received
//
//nolint:gocyclo // CLI dispatch has many small branches by design.
func runOneShotCmd(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()

	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(stderr, "usage: r1 --one-shot <verb> [--input <path>] [--max-mem N] [--timeout D] [--audit-endpoint URL] [--audit-token T] [--correlation-id ID]\n")
		fmt.Fprintf(stderr, "verbs: %s\n", strings.Join(oneshot.SupportedVerbs, ", "))
		return oneshot.ExitUsage
	}
	verb := args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("r1 --one-shot "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		inputPath         string
		jsonMode          bool
		maxMemMiB         int
		timeout           time.Duration
		auditEndpoint     string
		auditTokenFlag    string
		correlationIDFlag string
	)
	fs.StringVar(&inputPath, "input", "-", "path to JSON request payload; '-' for stdin")
	fs.BoolVar(&jsonMode, "json", false, "emit JSON output (accepted for CloudSwarm compatibility; one-shot output is always JSON)")
	fs.IntVar(&maxMemMiB, "max-mem", 256, "max memory (MiB) — Linux RLIMIT_AS + Go GOMEMLIMIT")
	fs.DurationVar(&timeout, "timeout", 60*time.Second, "per-call wall-clock timeout")
	fs.StringVar(&auditEndpoint, "audit-endpoint", "", "audit URL (empty disables)")
	fs.StringVar(&auditTokenFlag, "audit-token", "", "HMAC-SHA256 signing key")
	fs.StringVar(&correlationIDFlag, "correlation-id", "", "RelayGate trace id")
	if err := fs.Parse(rest); err != nil {
		return oneshot.ExitUsage
	}
	_ = jsonMode

	// --- Flag range validation -----------------------------------
	if maxMemMiB < minMaxMemMiB || maxMemMiB > maxMaxMemMiB {
		fmt.Fprintf(stderr, "oneshot: --max-mem must be in [%d, %d] MiB\n", minMaxMemMiB, maxMaxMemMiB)
		return oneshot.ExitUsage
	}
	if timeout < minTimeout || timeout > maxTimeout {
		fmt.Fprintf(stderr, "oneshot: --timeout must be in [%s, %s]\n", minTimeout, maxTimeout)
		return oneshot.ExitUsage
	}

	// --- Audit configuration -------------------------------------
	auditToken := auditTokenFlag
	if auditToken == "" {
		auditToken = os.Getenv("R1_AUDIT_TOKEN")
	}
	if auditEndpoint != "" {
		if !strings.HasPrefix(auditEndpoint, "https://") &&
			!strings.HasPrefix(auditEndpoint, "http://127.0.0.1") &&
			!strings.HasPrefix(auditEndpoint, "http://localhost") {
			fmt.Fprintln(stderr, "oneshot: --audit-endpoint must be https or http loopback")
			return oneshot.ExitUsage
		}
		if auditToken == "" {
			fmt.Fprintln(stderr, "oneshot: --audit-endpoint requires --audit-token or R1_AUDIT_TOKEN")
			return oneshot.ExitUsage
		}
	}

	// --- Correlation ID resolution -------------------------------
	// Precedence (per spec §T4.1): env R1_CORRELATION_ID >
	// --correlation-id flag > generated UUIDv4. The generated id is
	// RFC 4122 hex form so it round-trips through HTTP headers.
	correlationID := os.Getenv("R1_CORRELATION_ID")
	if correlationID == "" {
		correlationID = correlationIDFlag
	}
	if correlationID == "" {
		correlationID = generateCorrelationID()
	}

	// --- Memory limits -------------------------------------------
	// debug.SetPanicOnFault lets the deferred recover() below
	// catch ENOMEM-driven runtime faults; on a kernel-issued
	// SIGSEGV from an exhausted RLIMIT_AS the runtime panics with
	// "runtime: out of memory" which we pattern-match here.
	debug.SetPanicOnFault(true)
	if err := applyMemoryLimitWrapped(maxMemMiB); err != nil {
		fmt.Fprintf(stderr, `{"event":%q,"reason":"prlimit_failed","detail":%q,"max_mem_mib":%d,"correlation_id":%q}`+"\n",
			oneshot.EventMemoryLimitHit, err.Error(), maxMemMiB, correlationID)
		return oneshot.ExitMemory
	}

	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("%v", r)
			if strings.Contains(msg, "out of memory") || strings.Contains(msg, "cannot allocate memory") {
				fmt.Fprintf(stderr, `{"event":%q,"reason":"out_of_memory","max_mem_mib":%d,"correlation_id":%q}`+"\n",
					oneshot.EventMemoryLimitHit, maxMemMiB, correlationID)
				os.Exit(oneshot.ExitMemory)
			}
			// Non-memory panic — re-panic so the runtime prints
			// the canonical stack trace.
			panic(r)
		}
	}()

	// --- Signal-aware root context (T3.1) ------------------------
	sigCtx, sigStop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer sigStop()

	// Parallel sigCh captures which signal fired so the shutdown
	// envelope can name it. NotifyContext discards that info, so
	// we install a parallel handler.
	var firedSig atomic.Value
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		if s, ok := <-sigCh; ok {
			if ss, ok := s.(syscall.Signal); ok {
				firedSig.Store(ss)
			}
		}
	}()

	// --- Timeout (T2.1) ------------------------------------------
	ctx, cancel := context.WithTimeout(sigCtx, timeout)
	defer cancel()

	// --- Audit client (T4.4) -------------------------------------
	var audit *oneshot.AuditClient
	if auditEndpoint != "" {
		audit = oneshot.NewAuditClient(auditEndpoint, auditToken)
		defer func() {
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer drainCancel()
			sent, dropped := audit.DrainOrDrop(drainCtx)
			if dropped > 0 {
				fmt.Fprintf(stderr, `{"event":%q,"dropped":%d,"sent":%d,"correlation_id":%q}`+"\n",
					oneshot.EventAuditDropped, dropped, sent, correlationID)
			}
		}()
	}

	// --- Buffered stdout (T3.2) ----------------------------------
	// bufW is the eventual sink; bufCopy is the staging buffer
	// that holds the verb's encoded response until we decide
	// whether to flush (success) or drop (timeout / shutdown).
	bufW := bufio.NewWriter(stdout)
	defer func() { _ = bufW.Flush() }()

	var bufCopy bytes.Buffer

	// --- Read input payload (so we can SHA it for audit) ---------
	// Run the read on a goroutine so a signal arriving mid-read
	// can preempt a blocked stdin / FIFO. The signal-aware
	// ctx.Done() unblocks the select below and we fall through
	// to the shutdown-emit path with stdout untouched.
	type readResult struct {
		bytes []byte
		err   error
	}
	readCh := make(chan readResult, 1)
	go func() {
		b, e := readInputBytes(inputPath)
		readCh <- readResult{bytes: b, err: e}
	}()
	var payloadBytes []byte
	var payloadErr error
	select {
	case r := <-readCh:
		payloadBytes, payloadErr = r.bytes, r.err
	case <-sigCtx.Done():
		// Signal path — fall through to the shutdown-emit
		// section below by leaving payload empty and continuing
		// past the verb run.
		payloadBytes = nil
	}
	if payloadErr != nil {
		emitRuntimeError(stderr, verb, correlationID, payloadErr)
		return oneshot.ExitRuntime
	}

	// --- Run the verb --------------------------------------------
	resp, runErr := oneshot.Run(ctx, verb, bytes.NewReader(payloadBytes), &bufCopy)

	// --- Audit submission (regardless of err) --------------------
	if audit != nil {
		respBytes := bufCopy.Bytes()
		statusForAudit := "ok"
		if runErr != nil {
			statusForAudit = "error"
		} else if resp.Status == oneshot.StatusError {
			statusForAudit = "error"
		}
		env := oneshot.AuditEnvelope{
			Verb:            verb,
			Status:          statusForAudit,
			DurationMs:      time.Since(startedAt).Milliseconds(),
			CostEstimateUSD: resp.CostEstimateUSD,
			PayloadSHA256:   sha256Hex(payloadBytes),
			ResponseSHA256:  sha256Hex(respBytes),
			CorrelationID:   correlationID,
			SchemaVersion:   oneshot.AuditSchemaVersion,
			EmittedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		}
		audit.Submit(env)
	}

	// --- Timeout path (T2.3) — drop-partial ----------------------
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(stderr, `{"event":%q,"timeout":%q,"verb":%q,"correlation_id":%q}`+"\n",
			oneshot.EventTimeout, timeout.String(), verb, correlationID)
		return oneshot.ExitTimeout
	}

	// --- Shutdown path (T3.3) — drop-partial ---------------------
	if sigCtx.Err() != nil {
		sig := "UNKNOWN"
		code := oneshot.ExitRuntime
		// Drain firedSig with a short retry — the parallel
		// sigCh handler is a separate goroutine that races with
		// NotifyContext's ctx cancel, so we may arrive here
		// before the store landed. A short wait turns the race
		// into a deterministic resolve.
		resolved := func() (syscall.Signal, bool) {
			if v, ok := firedSig.Load().(syscall.Signal); ok {
				return v, true
			}
			// Fast-path drain of sigCh in case Notify queued
			// the signal but our handler goroutine hasn't run.
			select {
			case s := <-sigCh:
				if ss, ok := s.(syscall.Signal); ok {
					firedSig.Store(ss)
					return ss, true
				}
			case <-time.After(50 * time.Millisecond):
			}
			if v, ok := firedSig.Load().(syscall.Signal); ok {
				return v, true
			}
			return 0, false
		}
		if v, ok := resolved(); ok {
			switch v {
			case syscall.SIGINT:
				sig, code = "SIGINT", oneshot.ExitSIGINT
			case syscall.SIGTERM:
				sig, code = "SIGTERM", oneshot.ExitSIGTERM
			}
		}
		fmt.Fprintf(stderr, `{"event":%q,"signal":%q,"verb":%q,"correlation_id":%q}`+"\n",
			oneshot.EventShutdown, sig, verb, correlationID)
		return code
	}

	// --- Run-error path ------------------------------------------
	if runErr != nil {
		if errors.Is(runErr, oneshot.ErrUnknownVerb) {
			fmt.Fprintf(stderr, "%v\n", runErr)
			return oneshot.ExitUsage
		}
		emitRuntimeError(stderr, verb, correlationID, runErr)
		return oneshot.ExitRuntime
	}

	// --- Success path — flush staging buffer to stdout -----------
	if _, err := io.Copy(bufW, &bufCopy); err != nil {
		emitRuntimeError(stderr, verb, correlationID, err)
		return oneshot.ExitRuntime
	}
	return oneshot.ExitOK
}

// applyMemoryLimitWrapped is the test-overridable hook around
// oneshot.applyMemoryLimit. Indirected so the unit-tests can
// fake out the limit setter without touching the real RLIMIT_AS
// of the test runner process.
var applyMemoryLimitWrapped = func(maxMemMiB int) error {
	return applyMemLimitForCLI(maxMemMiB)
}

// readInputBytes drains the input source into memory. We stage
// the payload as []byte (not io.Reader) so we can both feed it
// to the verb AND compute its SHA256 for the audit envelope
// without consuming it twice.
func readInputBytes(inputPath string) ([]byte, error) {
	if inputPath == "" || inputPath == "-" {
		return io.ReadAll(os.Stdin)
	}
	f, err := os.Open(inputPath)
	if err != nil {
		return nil, fmt.Errorf("oneshot: open input: %w", err)
	}
	defer f.Close()
	return io.ReadAll(f)
}

// emitRuntimeError writes the legacy machine-readable error
// envelope to stderr. Format is intentionally identical to the
// pre-A3 envelope so CloudSwarm's existing supervisor parser
// keeps working; we just thread the correlation id through.
func emitRuntimeError(stderr io.Writer, verb, correlationID string, err error) {
	errEnv := map[string]string{
		"verb":           verb,
		"status":         "error",
		"error":          err.Error(),
		"correlation_id": correlationID,
	}
	if b, merr := json.Marshal(errEnv); merr == nil {
		fmt.Fprintln(stderr, string(b))
	} else {
		fmt.Fprintf(stderr, "oneshot error: %v\n", err)
	}
}

// sha256Hex returns the hex-encoded SHA256 of the input. Empty
// input returns the SHA of the empty string, which is a stable
// sentinel value RelayGate can recognize.
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// generateCorrelationID returns an RFC 4122 v4 UUID encoded as
// 32 lowercase hex chars (no dashes). Crypto/rand backed so the
// id survives a clock jump.
func generateCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Vanishingly unlikely; fall back to a timestamp-based
		// id so the audit envelope still has SOMETHING.
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	// RFC 4122 v4 bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[:])
}
