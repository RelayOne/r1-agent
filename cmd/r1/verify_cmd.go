package main

// verify_cmd.go -- work-stoke TASK 21.
//
// Exposes the verification-descent engine at
// internal/plan/verification_descent.go as a thin HTTP service.
//
//   POST /verify
//     request:  {output, criteria:[{id, command}]}
//     response: {passed, evidence[], tiers_attempted[]}
//
// One descent per request (per AC); concurrent requests each run in
// their own descent context. No shared mutable state across requests.
//
// Consumer: Truecom's LlmScreening runner, per specs/work-stoke.md.
// Default port is 9944 (work-stoke T21).
//
// T21 acceptance gates (verified green on commit):
//   - go build ./cmd/r1           exit 0
//   - go vet   ./cmd/r1           exit 0
//   - go test  ./cmd/r1           TestVerifyServe_* all PASS
//
// Concurrency model: each incoming POST /verify allocates its own
// DescentConfig + OnTierEvent collector per AC, so a fan-out of N
// simultaneous requests cannot leak tier events or pass flags across
// request boundaries. See TestVerifyServe_Concurrent for the property
// check.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/RelayOne/r1/internal/plan"
)

// VerifyRequest is the JSON body accepted by POST /verify.
type VerifyRequest struct {
	Output   string            `json:"output"`
	Criteria []VerifyCriterion `json:"criteria"`
}

// VerifyCriterion is a single acceptance criterion to descend.
type VerifyCriterion struct {
	ID      string `json:"id"`
	Command string `json:"command"`
}

// VerifyResponse is the JSON body returned by POST /verify.
//
//   passed          = every criterion resolved PASS or SOFT-PASS
//   evidence        = one human-readable line per criterion (same order
//                     as the request's criteria list)
//   tiers_attempted = union of tiers that fired across all criteria,
//                     sorted by tier number (e.g. ["T1","T2","T3"])
type VerifyResponse struct {
	Passed         bool     `json:"passed"`
	Evidence       []string `json:"evidence"`
	TiersAttempted []string `json:"tiers_attempted"`
}

// verifyCmd handles `r1 verify --serve`. The flag surface is kept
// small on purpose: one toggle to run as a server, one to pick the
// address. Future flags (TLS, auth) can slot in here without changing
// the server struct.
func verifyCmd(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	serve := fs.Bool("serve", false, "run as HTTP server instead of one-shot")
	addr := fs.String("addr", ":9944", "listen address when --serve is set")
	if err := fs.Parse(args); err != nil {
		fatal("verify: flag parse: %v", err)
	}

	if !*serve {
		fmt.Fprintln(os.Stderr, "r1 verify: --serve is required")
		fmt.Fprintln(os.Stderr, "    r1 verify --serve [--addr :9944]")
		os.Exit(2)
	}

	srv := newVerifyServer()
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr, "r1 verify --serve listening on %s\n", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "received %s, shutting down verify server...\n", sig)
	case err := <-errCh:
		if err != nil {
			fatal("verify --serve: %v", err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "verify shutdown: %v\n", err)
	}
}

// verifyServer is the HTTP-facing wrapper around plan.VerificationDescent.
// Stateless -- N concurrent /verify requests each construct their own
// DescentConfig and their own tier-event collector, so no cross-request
// state leaks or contention arises.
type verifyServer struct {
	// repoRoot is where AC commands execute. Default: cwd at startup.
	// Held on the server so all concurrent requests see a stable value
	// even if the process later chdirs (it won't, but the invariant is
	// cheap).
	repoRoot string
}

func newVerifyServer() *verifyServer {
	// LINT-ALLOW chdir-cli-entry: r1 verify HTTP server; cwd captured once at construction and stored in s.repoRoot so concurrent request handlers see a stable value (see struct doc above).
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	return &verifyServer{repoRoot: cwd}
}

func (s *verifyServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/verify", s.handleVerify)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// handleVerify decodes one VerifyRequest, runs one descent per AC, and
// writes one VerifyResponse. Errors on the input (bad method, bad JSON,
// empty criteria list) surface as 4xx before any descent fires.
func (s *verifyServer) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req VerifyRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}
	if len(req.Criteria) == 0 {
		http.Error(w, "bad request: criteria must be non-empty", http.StatusBadRequest)
		return
	}

	resp := s.runDescent(r.Context(), req)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// runDescent runs plan.VerificationDescent once per criterion and
// aggregates the results into a single response. Each descent gets its
// own OnTierEvent collector so two concurrent requests cannot scramble
// each other's tier list.
//
// Evidence per AC combines the final tier label, outcome, and a short
// tail of the raw AC output -- enough for a human operator to see why
// a criterion passed or failed without needing to re-run it.
func (s *verifyServer) runDescent(ctx context.Context, req VerifyRequest) VerifyResponse {
	allPassed := true
	evidence := make([]string, len(req.Criteria))
	tierUnion := map[plan.DescentTier]struct{}{}

	for i, c := range req.Criteria {
		ac := plan.AcceptanceCriterion{
			ID:          c.ID,
			Description: c.ID,
			Command:     c.Command,
		}

		// Per-request tier collector. Guarded because plan.VerificationDescent
		// is single-threaded per call but a caller-supplied OnTierEvent is
		// permitted to be invoked from goroutines in future revisions.
		var tierMu sync.Mutex
		tiersSeen := map[plan.DescentTier]struct{}{}

		cfg := plan.DescentConfig{
			RepoRoot: s.repoRoot,
			Session: plan.Session{
				ID:                 "verify-serve",
				Title:              "verify-serve",
				AcceptanceCriteria: []plan.AcceptanceCriterion{ac},
			},
			OnTierEvent: func(evt plan.DescentTierEvent) {
				tierMu.Lock()
				tiersSeen[evt.Tier] = struct{}{}
				tierMu.Unlock()
			},
			// Intentionally NO RepairFunc / EnvFixFunc / Provider: this
			// is a thin pass-through verifier, not a full repair agent.
			// T4/T5/T7 are no-ops in this mode; T1/T2/T3/T6/T8 still run.
		}

		result := plan.VerificationDescent(ctx, ac, req.Output, cfg)

		// A descent is "passed" for our API when it ended in either
		// DescentPass or DescentSoftPass. Hard fail is the only FALSE.
		acPassed := result.Outcome == plan.DescentPass || result.Outcome == plan.DescentSoftPass
		if !acPassed {
			allPassed = false
		}

		evidence[i] = formatEvidence(c.ID, result)

		tierMu.Lock()
		for t := range tiersSeen {
			tierUnion[t] = struct{}{}
		}
		tierMu.Unlock()

		// Belt-and-suspenders: if the descent resolved at a tier but
		// somehow no event fired (callback nil-check races in future
		// revisions), still record the resolved tier.
		if result.ResolvedAtTier != 0 {
			tierUnion[result.ResolvedAtTier] = struct{}{}
		}
	}

	return VerifyResponse{
		Passed:         allPassed,
		Evidence:       evidence,
		TiersAttempted: sortTiers(tierUnion),
	}
}

// formatEvidence collapses a DescentResult into one human-readable
// line for the response. Includes outcome, resolved tier, category
// (when classified), and a truncated tail of the raw AC output.
func formatEvidence(acID string, r plan.DescentResult) string {
	tail := r.RawACOutput
	const maxTail = 400
	if len(tail) > maxTail {
		tail = tail[len(tail)-maxTail:]
	}
	category := r.Category
	if category == "" {
		category = "-"
	}
	return fmt.Sprintf("%s: outcome=%s tier=%s category=%s reason=%s output=%q",
		acID, r.Outcome, r.ResolvedAtTier, category, r.Reason, tail)
}

// sortTiers returns a stable ["T1","T2",...] list from the set of
// tiers seen during one request. Order is by numeric tier index, which
// is the natural ladder order T1 -> T8.
func sortTiers(set map[plan.DescentTier]struct{}) []string {
	keys := make([]plan.DescentTier, 0, len(set))
	for t := range set {
		keys = append(keys, t)
	}
	sort.Slice(keys, func(i, j int) bool { return int(keys[i]) < int(keys[j]) })
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		// Convert "T1-intent-match" -> "T1" -- the short form in the
		// public API is the operator-friendly shape requested by the
		// Truecom LlmScreening consumer.
		full := k.String()
		if dash := indexByte(full, '-'); dash > 0 {
			out = append(out, full[:dash])
		} else {
			out = append(out, full)
		}
	}
	return out
}

// indexByte is a tiny helper to avoid importing strings just for one
// call. Returns -1 if c is not present in s.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
