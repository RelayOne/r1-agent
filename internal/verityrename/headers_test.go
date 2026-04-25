package verityrename

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDualSend_EmitsBoth asserts DualSend stamps both canonical and
// legacy header names with the same value (egress contract).
func TestDualSend_EmitsBoth(t *testing.T) {
	h := http.Header{}
	DualSend(h, ClientHeaderPair, "r1/1.0")

	if got := h.Get(CanonicalClientHeader); got != "r1/1.0" {
		t.Errorf("%s = %q; want %q", CanonicalClientHeader, got, "r1/1.0")
	}
	if got := h.Get(LegacyClientHeader); got != "r1/1.0" {
		t.Errorf("%s = %q; want %q", LegacyClientHeader, got, "r1/1.0")
	}
}

// TestDualSend_EmptyValueNoop asserts empty value is a no-op on both names.
func TestDualSend_EmptyValueNoop(t *testing.T) {
	h := http.Header{}
	DualSend(h, OrgHeaderPair, "")

	if got := h.Get(CanonicalOrgHeader); got != "" {
		t.Errorf("%s set from empty DualSend; want empty", CanonicalOrgHeader)
	}
	if got := h.Get(LegacyOrgHeader); got != "" {
		t.Errorf("%s set from empty DualSend; want empty", LegacyOrgHeader)
	}
}

// TestDualAccept_CanonicalWins asserts canonical beats legacy when both present.
func TestDualAccept_CanonicalWins(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(LegacyClientHeader, "old-client")
	req.Header.Set(CanonicalClientHeader, "new-client")

	got, ok := DualAccept(req, ClientHeaderPair)
	if !ok {
		t.Fatal("DualAccept returned ok=false with both headers present")
	}
	if got != "new-client" {
		t.Errorf("canonical must win; got %q", got)
	}
}

// TestDualAccept_LegacyFallback asserts legacy is accepted when canonical absent.
func TestDualAccept_LegacyFallback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(LegacyClientHeader, "verity-client/0.9")

	got, ok := DualAccept(req, ClientHeaderPair)
	if !ok {
		t.Fatal("DualAccept returned ok=false with legacy header present")
	}
	if got != "verity-client/0.9" {
		t.Errorf("legacy fallback: got %q; want %q", got, "verity-client/0.9")
	}
}

// TestDualAccept_BothAbsent asserts ("",false) when neither header present.
func TestDualAccept_BothAbsent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	got, ok := DualAccept(req, SignatureHeaderPair)
	if ok || got != "" {
		t.Errorf("both absent: got (%q,%v); want (\"\",false)", got, ok)
	}
}

// TestDualAccept_NilRequest asserts nil request is safe.
func TestDualAccept_NilRequest(t *testing.T) {
	got, ok := DualAccept(nil, ClientHeaderPair)
	if ok || got != "" {
		t.Errorf("nil request: got (%q,%v); want (\"\",false)", got, ok)
	}
}
