// Package main — stoke-a2a
//
// STOKE-013/-018 standalone A2A server. Hosts:
//
//   GET  /.well-known/agent-card.json — Agent Card (A2A v1.0 canonical)
//   GET  /.well-known/agent.json      — 308 Permanent Redirect to the
//                                        canonical path (legacy; sunset
//                                        2026-05-22, 30 days post-v1.0)
//   POST /a2a/rpc                     — JSON-RPC 2.0 task handlers
//   GET  /healthz                     — liveness
//
// Configuration via environment variables (keeps the binary
// dependency-free for container deployment):
//
//   STOKE_A2A_ADDR        listen address (default :7430)
//   STOKE_A2A_NAME        agent name (default "r1-a2a")
//   STOKE_A2A_DESC        human description
//   STOKE_A2A_VERSION     semver (default "dev")
//   STOKE_A2A_URL         external URL (for the card)
//   STOKE_A2A_PROVIDER    provider org name
//   STOKE_A2A_TOKEN       bearer token for /a2a/rpc (empty = open)
//   STOKE_A2A_CAPABILITIES comma-separated <name>@<version> list
//     (e.g. "search@1.0.0,plan@2.1.0") — these become the
//     CapabilityRef entries advertised in the card
//
// The Agent Card is built at startup. Operators wanting live
// capability updates can signal the process with SIGHUP —
// handled elsewhere via NewServer.SetCard. Keeping this
// binary minimal on purpose so it composes cleanly into
// larger deployments.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/RelayOne/r1/internal/a2a"
	"github.com/RelayOne/r1/internal/r1env"
)

func main() {
	// S1-1 env rename: canonical R1_A2A_* with STOKE_A2A_* fallback
	// through the 2026-07-23 dual-accept window.
	addr := envOr("R1_A2A_ADDR", "STOKE_A2A_ADDR", ":7430")
	name := envOr("R1_A2A_NAME", "STOKE_A2A_NAME", "r1-a2a")
	version := envOr("R1_A2A_VERSION", "STOKE_A2A_VERSION", "dev")
	desc := r1env.Get("R1_A2A_DESC", "STOKE_A2A_DESC")
	url := r1env.Get("R1_A2A_URL", "STOKE_A2A_URL")
	providerName := r1env.Get("R1_A2A_PROVIDER", "STOKE_A2A_PROVIDER")
	token := r1env.Get("R1_A2A_TOKEN", "STOKE_A2A_TOKEN")

	caps := parseCapabilities(r1env.Get("R1_A2A_CAPABILITIES", "STOKE_A2A_CAPABILITIES"))

	card := a2a.Build(a2a.Options{
		Name:         name,
		Description:  desc,
		Version:      version,
		URL:          url,
		Provider:     a2a.Provider{Name: providerName},
		Capabilities: caps,
		Endpoints: a2a.AgentEndpoints{
			JSONRPC: strings.TrimRight(url, "/") + "/a2a/rpc",
		},
	})
	store := a2a.NewInMemoryTaskStore()
	srv := a2a.NewServer(card, store, token)

	fmt.Fprintf(os.Stderr, "r1-a2a: listening on %s (capabilities=%d, auth=%t)\n",
		addr, len(caps), token != "")

	// Own the http.Server directly so SIGINT/SIGTERM can
	// trigger a graceful Shutdown (drain in-flight requests)
	// instead of killing the process mid-response.
	hs := &http.Server{Addr: addr, Handler: srv.Handler()}
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		fmt.Fprintln(os.Stderr, "r1-a2a: shutdown signal, draining")
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutCtx)
	}()
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "r1-a2a:", err)
		os.Exit(1)
	}
}

// parseCapabilities splits the comma-separated capability
// env var into CapabilityRef entries. Each entry is
// `<name>@<version>`; malformed entries are skipped with a
// warning to stderr so operators notice typos without
// breaking startup.
func parseCapabilities(s string) []a2a.CapabilityRef {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	fields := strings.Split(s, ",")
	out := make([]a2a.CapabilityRef, 0, len(fields))
	for _, raw := range fields {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.SplitN(raw, "@", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			fmt.Fprintf(os.Stderr, "r1-a2a: skip malformed capability %q (want name@version)\n", raw)
			continue
		}
		out = append(out, a2a.CapabilityRef{Name: parts[0], Version: parts[1]})
	}
	return out
}

// envOr resolves an env-var via r1env.Get (canonical R1_* first, legacy
// STOKE_* fallback with single-shot WARN) and substitutes def when
// both are unset. The dual-arg signature keeps call sites explicit
// about the rename pair they're participating in.
func envOr(canonical, legacy, def string) string {
	if v := r1env.Get(canonical, legacy); v != "" {
		return v
	}
	return def
}
