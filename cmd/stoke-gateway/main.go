// Package main — stoke-gateway
//
// STOKE-024 multi-platform messaging gateway binary. Wires
// internal/gateway's Router + ConversationCoordinator +
// CronScheduler to one or more PlatformAdapters from
// internal/gateway/platforms/ and starts the inbound fan-in
// + outbound fan-out loop.
//
// This binary is the transport host; the actual dialogue
// logic lives in whatever handler the operator plugs in via
// the Stoke HTTP API or MCP surface. The gateway converts
// platform messages to/from the agent-agnostic Message +
// Outbound shapes and delegates.
//
// Deliberately thin: real-world deployments will wire
// Telegram-BotAPI, Discord-gateway, Slack-socket-mode SDKs
// into the Client callback slot each platform exposes. Here
// we launch with the MemoryClient for each platform so a
// `stoke-gateway` invocation with no arguments starts a
// self-contained local-dev loopback suitable for testing
// the harness end-to-end without real platform accounts.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/ericmacdougall/stoke/internal/gateway"
	"github.com/ericmacdougall/stoke/internal/gateway/platforms"
)

// version is the binary version; -ldflags injects at
// release build time. Defaults to "dev" for local builds.
var version = "dev"

func main() {
	var (
		httpAddr = flag.String("http", "127.0.0.1:4040", "HTTP bind address for the gateway's local-dev loopback")
		secret   = flag.String("secret", "", "HMAC secret for pairing tokens (empty auto-generates; not stable across restarts)")
		showVer  = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println("stoke-gateway", version)
		return
	}

	router := gateway.NewRouter([]byte(*secret))

	// Register the three built-in platform adapters with
	// the in-memory client. Operators swap in real clients
	// by constructing the router themselves (this binary
	// is the local-dev default).
	router.Register(platforms.NewTelegram(platforms.NewMemoryClient()))
	router.Register(platforms.NewDiscord(platforms.NewMemoryClient()))
	router.Register(platforms.NewSlack(platforms.NewMemoryClient()))

	// Inbound handler: for the local-dev binary, just log.
	// Production gateway wraps this in a dispatch to the
	// Stoke agent surface.
	handler := func(ctx context.Context, m gateway.Message) error {
		log.Printf("inbound: platform=%s cid=%s sender=%s text=%q",
			m.Platform, m.ConversationID, m.SenderPlatformID, m.Text)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// HTTP side-channel: lets operators issue pairing
	// tokens + inspect conversation history via plain HTTP
	// while the adapters run.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pair", func(w http.ResponseWriter, r *http.Request) {
		cid := r.URL.Query().Get("conversation_id")
		if cid == "" {
			http.Error(w, "conversation_id required", http.StatusBadRequest)
			return
		}
		tok := router.IssuePairingToken(cid, 15*60*1_000_000_000) // 15 min
		_ = json.NewEncoder(w).Encode(map[string]string{"token": string(tok)})
	})
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + version + `"}`))
	})

	httpSrv := &http.Server{Addr: *httpAddr, Handler: mux}
	go func() {
		log.Printf("stoke-gateway %s listening on %s (platforms: %v)",
			version, *httpAddr, router.Registered())
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http: %v", err)
		}
	}()

	// Graceful shutdown on signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
		_ = httpSrv.Shutdown(context.Background())
	}()

	if err := router.StartAll(ctx, handler); err != nil && ctx.Err() == nil {
		log.Printf("gateway: %v", err)
		os.Exit(1)
	}
}
