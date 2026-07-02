package pick

import (
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/browser"
	"github.com/RelayOne/r1/internal/browser/browserless"
	"github.com/RelayOne/r1/internal/browser/inhouse"
)

func TestPickProviderDefaultsToLocal(t *testing.T) {
	for _, name := range []string{"", "local", "LOCAL", "  local  "} {
		p, err := PickProvider(name, Config{})
		if err != nil {
			t.Fatalf("PickProvider(%q): %v", name, err)
		}
		if got := p.Name(); got != browser.ProviderLocal {
			t.Errorf("PickProvider(%q).Name() = %q, want %q", name, got, browser.ProviderLocal)
		}
	}
}

func TestPickProviderUsesConfigWhenNameEmpty(t *testing.T) {
	cfg := Config{
		Provider:    browser.ProviderBrowserless,
		Browserless: browserless.Config{Endpoint: "wss://chrome.example.com"},
	}
	p, err := PickProvider("", cfg)
	if err != nil {
		t.Fatalf("PickProvider: %v", err)
	}
	if got := p.Name(); got != browser.ProviderBrowserless {
		t.Errorf("Name() = %q, want %q", got, browser.ProviderBrowserless)
	}
}

func TestPickProviderBrowserless(t *testing.T) {
	p, err := PickProvider(browser.ProviderBrowserless, Config{
		Browserless: browserless.Config{Endpoint: "wss://chrome.example.com"},
	})
	if err != nil {
		t.Fatalf("PickProvider(browserless): %v", err)
	}
	if got := p.Name(); got != browser.ProviderBrowserless {
		t.Errorf("Name() = %q, want %q", got, browser.ProviderBrowserless)
	}
}

func TestPickProviderBrowserlessInvalidConfig(t *testing.T) {
	if _, err := PickProvider(browser.ProviderBrowserless, Config{}); err == nil {
		t.Fatal("PickProvider(browserless) with empty endpoint: want error, got nil")
	}
}

func TestPickProviderInhouse(t *testing.T) {
	p, err := PickProvider(browser.ProviderInhouse, Config{
		Inhouse: inhouse.Config{Endpoint: "wss://browser.example.com"},
	})
	if err != nil {
		t.Fatalf("PickProvider(inhouse): %v", err)
	}
	if got := p.Name(); got != browser.ProviderInhouse {
		t.Errorf("Name() = %q, want %q", got, browser.ProviderInhouse)
	}
}

func TestPickProviderInhouseInvalidConfig(t *testing.T) {
	if _, err := PickProvider(browser.ProviderInhouse, Config{}); err == nil {
		t.Fatal("PickProvider(inhouse) with empty endpoint: want error, got nil")
	}
}

func TestPickProviderUnknownNameFailsHard(t *testing.T) {
	_, err := PickProvider("chromium-cloud", Config{})
	if err == nil {
		t.Fatal("PickProvider(unknown): want error, got nil (silent local downgrade is forbidden)")
	}
	if !strings.Contains(err.Error(), "chromium-cloud") {
		t.Errorf("error %q should name the unknown provider", err)
	}
}

func TestPickProviderWrapsFallback(t *testing.T) {
	p, err := PickProvider(browser.ProviderBrowserless, Config{
		Fallback:    browser.ProviderLocal,
		Browserless: browserless.Config{Endpoint: "wss://chrome.example.com"},
	})
	if err != nil {
		t.Fatalf("PickProvider with fallback: %v", err)
	}
	if _, ok := p.(*browser.FallbackProvider); !ok {
		t.Fatalf("provider type = %T, want *browser.FallbackProvider", p)
	}
	// Name attribution stays with the primary (spec C6 T8).
	if got := p.Name(); got != browser.ProviderBrowserless {
		t.Errorf("Name() = %q, want primary %q", got, browser.ProviderBrowserless)
	}
}

func TestPickProviderFallbackSameAsPrimaryRejected(t *testing.T) {
	_, err := PickProvider(browser.ProviderLocal, Config{Fallback: "local"})
	if err == nil {
		t.Fatal("fallback == primary: want error, got nil")
	}
}

func TestPickProviderFallbackInvalidConfigRejected(t *testing.T) {
	// Primary constructs fine, but the named fallback has no usable
	// config — the factory must surface that instead of silently
	// dropping the fallback.
	_, err := PickProvider(browser.ProviderLocal, Config{Fallback: browser.ProviderBrowserless})
	if err == nil {
		t.Fatal("invalid fallback config: want error, got nil")
	}
}
