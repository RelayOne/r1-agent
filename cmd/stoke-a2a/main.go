// Package main — stoke-a2a
//
// STOKE-013/-018 standalone A2A server. Hosts:
//
//   GET  /.well-known/agent.json  — Agent Card for discovery
//   POST /a2a/rpc                 — JSON-RPC 2.0 task handlers
//   GET  /healthz                 — liveness
//
// Configuration via environment variables (keeps the binary
// dependency-free for container deployment):
//
//   STOKE_A2A_ADDR        listen address (default :7430)
//   STOKE_A2A_NAME        agent name (default "stoke-a2a")
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

	"github.com/ericmacdougall/stoke/internal/a2a"
)

func main() {
	addr := envOr("STOKE_A2A_ADDR", ":7430")
	name := envOr("STOKE_A2A_NAME", "stoke-a2a")
	version := envOr("STOKE_A2A_VERSION", "dev")
	desc := os.Getenv("STOKE_A2A_DESC")
	url := os.Getenv("STOKE_A2A_URL")
	providerName := os.Getenv("STOKE_A2A_PROVIDER")
	token := os.Getenv("STOKE_A2A_TOKEN")

	caps := parseCapabilities(os.Getenv("STOKE_A2A_CAPABILITIES"))

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

	fmt.Fprintf(os.Stderr, "stoke-a2a: listening on %s (capabilities=%d, auth=%t)\n",
		addr, len(caps), token != "")

	// Own the http.Server directly so SIGINT/SIGTERM can
	// trigger a graceful Shutdown (drain in-flight requests)
	// instead of killing the process mid-response.
	hs := &http.Server{Addr: addr, Handler: srv.Handler()}
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		fmt.Fprintln(os.Stderr, "stoke-a2a: shutdown signal, draining")
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutCtx)
	}()
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "stoke-a2a:", err)
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
	var out []a2a.CapabilityRef
	for _, raw := range strings.Split(s, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.SplitN(raw, "@", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			fmt.Fprintf(os.Stderr, "stoke-a2a: skip malformed capability %q (want name@version)\n", raw)
			continue
		}
		out = append(out, a2a.CapabilityRef{Name: parts[0], Version: parts[1]})
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
