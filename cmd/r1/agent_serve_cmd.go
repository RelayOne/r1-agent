package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RelayOne/r1/internal/agentserve"
	"github.com/RelayOne/r1/internal/deploy"
	"github.com/RelayOne/r1/internal/executor"
	"github.com/RelayOne/r1/internal/r1env"
	"github.com/RelayOne/r1/internal/truecom"
)

// agentServeCmd handles `r1 agent-serve` — the hireable-agent
// HTTP facade (Task 24). Distinct from `r1 serve`, which runs
// the mission-orchestrator API used by stoke-server / dashboards.
//
// Endpoints:
//   GET  /api/capabilities          advertise what this Stoke can do
//   POST /api/task                  submit + run a task synchronously
//   GET  /api/task/{id}             poll an already-submitted task
//
// Auth: STOKE_SERVE_TOKENS env (CSV) sets accepted bearer tokens.
// Empty → no auth (localhost dev only — do NOT expose publicly).
//
// TrustPlane (TASK-T20): `--trustplane-register` enables an outbound
// RegisterCapabilities call on startup. When wired, terminal task
// transitions trigger Settle (on pass) or Dispute (on fail) against
// the gateway, with contract_id pulled from TaskRequest.Extra. All
// TrustPlane integration is fail-soft: registration errors log and
// the server continues to run.
func agentServeCmd(args []string) {
	opts, err := parseAgentServeFlags(args)
	if err != nil {
		fatal("agent-serve: %v", err)
	}

	registry := buildExecutorRegistry()
	bearer := parseTokens(r1env.Get("R1_SERVE_TOKENS", "STOKE_SERVE_TOKENS"))

	cfg := agentserve.Config{
		Version:      version,
		Capabilities: agentserve.Capabilities{TaskTypes: opts.advertised},
		Executors:    registry,
		Bearer:       bearer,
		TaskTimeout:  opts.timeout,
	}

	s := agentserve.NewServer(cfg)

	// TASK-T20 — optional TrustPlane registration. When
	// --trustplane-register is set we build a real client, register
	// advertised capabilities, and wire OnTaskComplete to Settle /
	// Dispute. Failures log and flow continues (fail-soft per spec):
	// a flaky gateway must never stop the hireable-agent from serving
	// local tasks.
	if opts.trustplaneRegister {
		regCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		tp, did, regErr := registerWithTrustPlane(regCtx, opts, s)
		cancel()
		if regErr != nil {
			fmt.Fprintf(os.Stderr, "trustplane register: %v (continuing)\n", regErr)
		} else {
			fmt.Fprintf(os.Stderr, "registered as %s\n", did)
			s.SetOnTaskComplete(buildSettlementCallback(s, tp, did))
		}
	}

	srv := &http.Server{
		Addr:              opts.addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr,
			"r1 agent-serve listening on %s (task types: %d, auth: %v)\n",
			opts.addr, len(registry), len(bearer) > 0)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "received %s, shutting down...\n", sig)
	case err := <-errCh:
		if err != nil {
			fatal("agent-serve: %v", err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
	}
}

// agentServeOpts captures every parsed CLI flag for agent-serve so
// test code and the run path share one shape. trustplaneEndpoint
// falls back to STOKE_TRUSTPLANE_ENDPOINT when the flag is empty so
// operators can pin the gateway via env alone.
type agentServeOpts struct {
	addr               string
	timeout            time.Duration
	advertised         []string
	trustplaneRegister bool
	trustplaneEndpoint string
	trustplaneDID      string
	trustplaneAgentID  string
}

// parseAgentServeFlags is separated from agentServeCmd so
// TestAgentServe_TrustPlaneRegisterFlag_ParsedOK can exercise the flag
// surface without spinning up the listener. Uses flag.ContinueOnError
// so tests don't os.Exit when flag.Parse rejects input.
func parseAgentServeFlags(args []string) (agentServeOpts, error) {
	fs := flag.NewFlagSet("agent-serve", flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))
	addr := fs.String("addr", ":8440", "listen address")
	timeout := fs.Duration("task-timeout", 10*time.Minute, "per-task execution deadline")
	caps := fs.String("caps", "", "advertised task types (CSV); empty = all registered")
	tpReg := fs.Bool("trustplane-register", false, "register advertised capabilities with TrustPlane on startup")
	tpEndpoint := fs.String("trustplane-endpoint", "", "TrustPlane gateway base URL (falls back to STOKE_TRUSTPLANE_ENDPOINT)")
	tpDID := fs.String("trustplane-did", "", "instance DID to present during registration (falls back to STOKE_TRUSTPLANE_DID)")
	tpAgentID := fs.String("trustplane-agent-id", "", "stable agent_id for the capability record (falls back to STOKE_TRUSTPLANE_AGENT_ID)")

	if err := fs.Parse(args); err != nil {
		return agentServeOpts{}, err
	}

	advertised := cleanCSV(*caps)
	endpoint := *tpEndpoint
	if endpoint == "" {
		endpoint = strings.TrimSpace(r1env.Get("R1_TRUSTPLANE_ENDPOINT", "STOKE_TRUSTPLANE_ENDPOINT"))
	}
	did := *tpDID
	if did == "" {
		did = strings.TrimSpace(r1env.Get("R1_TRUSTPLANE_DID", "STOKE_TRUSTPLANE_DID"))
	}
	agentID := *tpAgentID
	if agentID == "" {
		agentID = strings.TrimSpace(r1env.Get("R1_TRUSTPLANE_AGENT_ID", "STOKE_TRUSTPLANE_AGENT_ID"))
	}

	return agentServeOpts{
		addr:               *addr,
		timeout:            *timeout,
		advertised:         advertised,
		trustplaneRegister: *tpReg,
		trustplaneEndpoint: endpoint,
		trustplaneDID:      did,
		trustplaneAgentID:  agentID,
	}, nil
}

// cleanCSV splits a comma-separated list and returns the trimmed
// non-empty entries. Shared between --caps parsing and other CLI
// flags that accept token lists.
func cleanCSV(in string) []string {
	if strings.TrimSpace(in) == "" {
		return nil
	}
	parts := strings.Split(in, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// registerWithTrustPlane builds a RealClient from the configured
// endpoint + Ed25519 key, submits a capability record derived from
// the server's advertised surface, and returns the client + DID so
// the caller can wire settlement callbacks. Fails closed only when
// the key material is unreadable; everything else propagates as an
// error for the caller to log-and-continue.
func registerWithTrustPlane(ctx context.Context, opts agentServeOpts, s *agentserve.Server) (*truecom.RealClient, string, error) {
	endpoint := strings.TrimSpace(opts.trustplaneEndpoint)
	if endpoint == "" {
		return nil, "", errors.New("trustplane: --trustplane-endpoint (or STOKE_TRUSTPLANE_ENDPOINT) is required with --trustplane-register")
	}
	did := strings.TrimSpace(opts.trustplaneDID)
	if did == "" {
		return nil, "", errors.New("trustplane: --trustplane-did (or STOKE_TRUSTPLANE_DID) is required with --trustplane-register")
	}

	priv, err := loadTrustPlanePrivateKey()
	if err != nil {
		return nil, "", fmt.Errorf("trustplane: load private key: %w", err)
	}
	signer, err := truecom.NewIdentitySigner(did, priv)
	if err != nil {
		return nil, "", fmt.Errorf("trustplane: identity signer: %w", err)
	}
	client, err := truecom.NewRealClient(truecom.RealClientOptions{
		BaseURL:    endpoint,
		PrivateKey: priv,
	})
	if err != nil {
		return nil, "", fmt.Errorf("trustplane: real client: %w", err)
	}
	client.WithIdentity(signer)

	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		// priv is guaranteed ed25519.PrivateKey by the parser above;
		// a surprise type here is a programming error.
		return nil, "", fmt.Errorf("trustplane: unexpected public-key type %T", priv.Public())
	}
	reg, err := capabilityRegistration(s, opts, did, pub)
	if err != nil {
		return nil, "", err
	}

	if _, err := client.RegisterCapabilities(ctx, reg); err != nil {
		return nil, "", fmt.Errorf("trustplane: register: %w", err)
	}
	return client, did, nil
}

// capabilityRegistration builds the CapabilityRegistration payload
// the gateway stores for discovery + settlement routing. Task types
// come from the server's advertised Capabilities (falling back to
// the registered executors) so the wire view matches what
// /api/capabilities returns.
func capabilityRegistration(s *agentserve.Server, opts agentServeOpts, did string, pub ed25519.PublicKey) (truecom.CapabilityRegistration, error) {
	jwk, err := truecom.BuildPublicKeyJWK(pub)
	if err != nil {
		return truecom.CapabilityRegistration{}, fmt.Errorf("trustplane: build JWK: %w", err)
	}
	taskTypes := s.Config().Capabilities.TaskTypes
	if len(taskTypes) == 0 {
		for t := range s.Config().Executors {
			taskTypes = append(taskTypes, t.String())
		}
	}
	agentID := strings.TrimSpace(opts.trustplaneAgentID)
	if agentID == "" {
		// Deterministic fallback: hex-suffix the DID so repeated
		// startups with the same DID register under the same agent_id.
		agentID = "stoke-agent-" + strconv.Itoa(len(did))
	}
	return truecom.CapabilityRegistration{
		DID:          did,
		AgentID:      agentID,
		Version:      version,
		TaskTypes:    taskTypes,
		Endpoint:     opts.addr,
		PublicKeyJWK: jwk,
	}, nil
}

// loadTrustPlanePrivateKey resolves the Ed25519 private key used for
// DPoP + identity header signing. Resolution order:
//
//  1. STOKE_TRUSTPLANE_PRIVKEY — inline PEM string (PKCS#8).
//  2. STOKE_TRUSTPLANE_PRIVKEY_FILE — path to a PEM file (PKCS#8).
//
// Returns a typed error when both are unset so the caller reports a
// clear configuration gap to operators.
func loadTrustPlanePrivateKey() (ed25519.PrivateKey, error) {
	inline := strings.TrimSpace(r1env.Get("R1_TRUSTPLANE_PRIVKEY", "STOKE_TRUSTPLANE_PRIVKEY"))
	if inline != "" {
		return decodeEd25519PEM(inline)
	}
	path := strings.TrimSpace(r1env.Get("R1_TRUSTPLANE_PRIVKEY_FILE", "STOKE_TRUSTPLANE_PRIVKEY_FILE"))
	if path == "" {
		return nil, errors.New("neither R1_TRUSTPLANE_PRIVKEY / STOKE_TRUSTPLANE_PRIVKEY nor R1_TRUSTPLANE_PRIVKEY_FILE / STOKE_TRUSTPLANE_PRIVKEY_FILE set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return decodeEd25519PEM(string(raw))
}

// decodeEd25519PEM decodes a PEM block containing a PKCS#8-wrapped
// Ed25519 private key. Matches trustplane/factory.go's parser so the
// two code paths accept the same operator input.
func decodeEd25519PEM(pemStr string) (ed25519.PrivateKey, error) {
	trimmed := strings.TrimSpace(pemStr)
	if trimmed == "" {
		return nil, errors.New("empty PEM input")
	}
	block, _ := pem.Decode([]byte(trimmed))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS#8: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PEM key is not Ed25519 (got %T)", key)
	}
	return priv, nil
}

// buildSettlementCallback returns an OnTaskComplete function that:
//
//  1. Posts Settle on pass or Dispute on fail for contract-bound tasks
//     (contract_id must be present in TaskRequest.Extra).
//  2. Emits a SOWReceipt to TrustPlane for ALL terminal tasks
//     (fail-soft, fire-and-forget goroutine, source="r1" always set).
//
// contract_id is pulled from the task's TaskRequest.Extra (see
// Server.TaskMetadata); tasks without a contract_id skip settlement
// but still emit the SOW receipt.
//
// All outbound calls run under a bounded ctx and failures log through
// stderr — callers never see an error because the server has already
// persisted the terminal transition.
func buildSettlementCallback(s *agentserve.Server, tp *truecom.RealClient, did string) func(string, bool, [][]byte) {
	return func(taskID string, passed bool, evidence [][]byte) {
		meta := s.TaskMetadata(taskID)
		contractID := stringFromMeta(meta, "contract_id")

		// --- F-W1-3: emit SOW receipt to TrustPlane for every terminal task ---
		// Runs in a goroutine so a gateway outage never blocks the caller.
		// source is forced to "r1" by EmitSOWReceipt regardless of what we
		// set here; included for clarity.
		outcome := "pass"
		if !passed {
			outcome = "fail"
		}
		go func() {
			rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer rcancel()
			receipt := truecom.SOWReceipt{
				TaskID:     taskID,
				ContractID: contractID,
				AgentDID:   did,
				Outcome:    outcome,
				TaskType:   stringFromMeta(meta, "task_type"),
				CostUSD:    floatFromMeta(meta, "amount_usd"),
			}
			if _, err := tp.EmitSOWReceipt(rctx, receipt); err != nil {
				fmt.Fprintf(os.Stderr, "truecom emit receipt %s: %v\n", taskID, err)
			}
		}()

		// --- Settlement (only for contract-bound tasks) ---
		if contractID == "" {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if passed {
			amount := floatFromMeta(meta, "amount_usd")
			if _, err := tp.Settle(ctx, truecom.SettleRequestBody{
				ContractID: contractID,
				AgentDID:   did,
				AmountUSD:  amount,
				Note:       "agent-serve task " + taskID + " passed",
			}); err != nil {
				fmt.Fprintf(os.Stderr, "trustplane settle %s: %v\n", contractID, err)
			}
			return
		}

		disputeReason := "agent-serve task " + taskID + " failed"
		if len(evidence) > 0 {
			disputeReason = string(evidence[0])
		}
		evidenceJSON := make([]json.RawMessage, 0, len(evidence))
		for _, e := range evidence {
			buf, err := json.Marshal(map[string]any{"sample": string(e)})
			if err == nil {
				evidenceJSON = append(evidenceJSON, buf)
			}
		}
		if _, err := tp.Dispute(ctx, truecom.DisputeRequestBody{
			ContractID:   contractID,
			AgentDID:     did,
			FailedReason: disputeReason,
			Verdicts:     evidenceJSON,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "trustplane dispute %s: %v\n", contractID, err)
		}
	}
}

// stringFromMeta pulls a string value out of the TaskRequest.Extra
// blob. Non-string values degrade to empty because the callback
// treats the missing/invalid case identically (skip settlement).
func stringFromMeta(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	v, ok := meta[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// floatFromMeta accepts both JSON numbers (float64) and strings so
// operators can submit `"amount_usd": "1.25"` without the server
// parsing getting in the way.
func floatFromMeta(meta map[string]any, key string) float64 {
	if meta == nil {
		return 0
	}
	switch v := meta[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

// buildExecutorRegistry populates the executor map used by the
// agent-serve server. Every wired executor from Tasks 20-22 is
// registered; future executors (Task 23 delegation) slot in here.
//
// Task 24 keeps registry construction on this single surface so a
// future refactor can move it into internal/executor/ without
// touching agentserve or the CLI.
func buildExecutorRegistry() map[executor.TaskType]executor.Executor {
	return map[executor.TaskType]executor.Executor{
		executor.TaskResearch: executor.NewResearchExecutor(nil),
		executor.TaskBrowser:  executor.NewBrowserExecutor(),
		executor.TaskDeploy:   executor.NewDeployExecutor(deploy.DeployConfig{}),
		// CodeExecutor lives in sow_native.go; direct task dispatch
		// via agent-serve is part of Task 24's follow-up once the
		// code path moves behind the executor interface.
	}
}

// parseTokens splits a CSV token list and trims whitespace. Empty
// input → nil (no auth). Used by both agent-serve and tests.
func parseTokens(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
