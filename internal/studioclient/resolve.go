package studioclient

import (
	"fmt"
	"net/http"

	"github.com/RelayOne/r1/internal/config"
)

// Resolve returns the Transport implementing the active studio_config.
// Intended to be called once per R1 session and the result reused for
// all skill invocations so HTTP keep-alives / stdio subprocesses are
// amortized.
//
// The signature accepts an optional *http.Client and EventPublisher.
// Pass nil for either to get defaults: http.Client{} and no telemetry.
func Resolve(cfg config.StudioConfig, httpClient *http.Client, pub EventPublisher) (Transport, error) {
	if !cfg.Enabled {
		return nil, ErrStudioDisabled
	}
	switch cfg.ResolvedTransport() {
	case config.StudioTransportHTTP:
		return NewHTTPTransport(cfg, httpClient, pub)
	case config.StudioTransportStdioMCP:
		return NewStdioMCPTransport(cfg, pub)
	default:
		return nil, fmt.Errorf("studioclient: transport %q not supported", cfg.ResolvedTransport())
	}
}
