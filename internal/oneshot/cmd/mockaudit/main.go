// mockaudit — minimal HTTP server that accepts the audit POSTs
// emitted by `r1 --one-shot`. Used by the operator runbook in
// docs/integrations/relaygate-r1-stage.md and by the
// TestRunbook_LocalMockRelayGate integration test.
//
// Verifies the HMAC-SHA256 signature against a shared secret,
// logs every accepted POST with its correlation id, and returns
// 200. Reject (401) on signature mismatch.
//
// Usage:
//
//	go run ./internal/oneshot/cmd/mockaudit -addr 127.0.0.1:9111 -token devtoken
//
// Spec: specs/oneshot-production-hardening.md §T6.7.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/RelayOne/r1/internal/oneshot"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9111", "listen address")
	token := flag.String("token", "devtoken", "HMAC-SHA256 shared secret")
	flag.Parse()

	logger := log.New(os.Stdout, "mockaudit ", log.LstdFlags|log.Lmicroseconds)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
			return
		}
		sig := r.Header.Get(oneshot.AuditSigHeader)
		mac := hmac.New(sha256.New, []byte(*token))
		mac.Write(body)
		want := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(sig), []byte(want)) {
			logger.Printf("REJECT sig-mismatch correlation_id=%s got=%s want=%s",
				r.Header.Get(oneshot.AuditCorrelationIDHeader), sig, want)
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		var env oneshot.AuditEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			http.Error(w, "json: "+err.Error(), http.StatusBadRequest)
			return
		}
		logger.Printf("ACCEPT verb=%s status=%s duration_ms=%d correlation_id=%s payload_sha256=%s response_sha256=%s schema=%s",
			env.Verb, env.Status, env.DurationMs, env.CorrelationID,
			env.PayloadSHA256, env.ResponseSHA256, env.SchemaVersion)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"ok":true}`)
	})

	logger.Printf("listening on %s (token=%s)", *addr, *token)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		logger.Fatalf("listen: %v", err)
	}
}
