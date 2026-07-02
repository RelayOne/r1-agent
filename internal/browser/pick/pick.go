// Package pick implements the construction-time Provider factory for
// the browser package (spec C6 §"Selection happens at construction
// time"): it maps the `browser.provider` config names — "local",
// "browserless", "inhouse" — to constructed browser.Provider
// implementations, optionally wrapped in the FallbackProvider
// (spec C6 T8) when a fallback provider is named.
//
// The factory lives in its own package rather than in package browser
// because the remote implementations (browserless/, inhouse/) import
// package browser for the Provider interface — a factory inside
// package browser would be an import cycle. cmd/r1 (browse_cmd) and
// internal/executor are the intended callers.
package pick

import (
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/browser"
	"github.com/RelayOne/r1/internal/browser/browserless"
	"github.com/RelayOne/r1/internal/browser/inhouse"
)

// Config carries the per-provider construction inputs. Only the
// section for the selected provider (and, when set, the fallback
// provider) is consulted; the rest may stay zero-valued.
type Config struct {
	// Provider is the configured provider name (`browser.provider`).
	// Used when PickProvider's name argument is empty. Empty → local.
	Provider string

	// Fallback optionally names a secondary provider. When set, the
	// primary is wrapped in browser.NewFallback so transient primary
	// failures (browser.ErrProviderUnreachable) route to the
	// secondary. Must differ from the resolved primary name.
	// Prod gating of local-fallback (spec T8a,
	// browser.fallback_allow_in_prod) is the config loader's job,
	// not this factory's.
	Fallback string

	// Local configures the in-process provider (provider_local.go).
	Local browser.LocalConfig

	// Browserless configures the Browserless CDP-over-WS provider.
	Browserless browserless.Config

	// Inhouse configures the r1-browser Cloud Run provider.
	Inhouse inhouse.Config

	// Hub, when non-nil, receives browser.fallback_used events from
	// the fallback wrapper. Nil disables event emission.
	Hub *browser.Hub
}

// PickProvider constructs the Provider selected by name — the
// dispatch documented in provider.go. name is typically the
// --provider CLI override; when empty, cfg.Provider decides, and when
// that is also empty the local provider is used (the pre-C6 default,
// so unconfigured installs keep working).
//
// Unknown names fail hard rather than silently downgrading to local:
// a silent downgrade would bypass the sandbox/egress guarantees the
// operator asked for by naming a remote provider.
func PickProvider(name string, cfg Config) (browser.Provider, error) {
	if strings.TrimSpace(name) == "" {
		name = cfg.Provider
	}
	primary, err := construct(name, cfg)
	if err != nil {
		return nil, err
	}

	fallback := strings.ToLower(strings.TrimSpace(cfg.Fallback))
	if fallback == "" {
		return primary, nil
	}
	if fallback == primary.Name() {
		return nil, fmt.Errorf("browser: fallback provider %q must differ from primary %q", cfg.Fallback, primary.Name())
	}
	secondary, err := construct(fallback, cfg)
	if err != nil {
		return nil, fmt.Errorf("browser: construct fallback provider: %w", err)
	}
	return browser.NewFallback(primary, secondary, cfg.Hub), nil
}

// construct maps one provider name to its constructor. Empty name →
// local (the safe, dependency-free default).
func construct(name string, cfg Config) (browser.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", browser.ProviderLocal:
		return browser.NewLocalProvider(cfg.Local), nil
	case browser.ProviderBrowserless:
		return browserless.NewClient(cfg.Browserless)
	case browser.ProviderInhouse:
		return inhouse.NewClient(cfg.Inhouse)
	default:
		return nil, fmt.Errorf("browser: unknown provider %q (want %s|%s|%s)",
			name, browser.ProviderLocal, browser.ProviderBrowserless, browser.ProviderInhouse)
	}
}
