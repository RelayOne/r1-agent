package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockServer mimics PostHog's /capture, /batch, /e and /i endpoints.
// Captured request bodies are stored in the recorder for assertion.
type mockServer struct {
	t           *testing.T
	srv         *httptest.Server
	mu          sync.Mutex
	requests    []map[string]any
	requestPath []string
	count       atomic.Int64
}

func newMockServer(t *testing.T) *mockServer {
	t.Helper()
	m := &mockServer{t: t}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)

		m.mu.Lock()
		m.requestPath = append(m.requestPath, r.URL.Path)
		if batch, ok := payload["batch"].([]any); ok {
			for _, item := range batch {
				if obj, ok := item.(map[string]any); ok {
					m.requests = append(m.requests, obj)
					m.count.Add(1)
				}
			}
		} else if len(payload) > 0 {
			m.requests = append(m.requests, payload)
			m.count.Add(1)
		}
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockServer) Events() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, len(m.requests))
	copy(out, m.requests)
	return out
}

func drainEvents(t *testing.T, c *Client, m *mockServer) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.Shutdown(ctx)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && m.count.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	return m.Events()
}

func TestFromEnv_NoKey_ReturnsNoOp(t *testing.T) {
	t.Setenv("POSTHOG_API_KEY", "")
	c := FromEnv("r1-test")
	if c == nil {
		t.Fatal("FromEnv returned nil")
	}
	if c.Enabled() {
		t.Fatal("expected !Enabled() with empty POSTHOG_API_KEY")
	}
	c.Capture(context.Background(), "anon", "session_started", nil)
	c.Identify(context.Background(), "anon", nil)
	c.Group(context.Background(), "tenant", "t1", nil)
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on no-op returned err: %v", err)
	}
}

func TestFromEnv_AnalyticsDisabled_ReturnsNoOp(t *testing.T) {
	t.Setenv("POSTHOG_API_KEY", "phc_realkey")
	t.Setenv("ANALYTICS_DISABLED", "1")
	c := FromEnv("r1-test")
	if c.Enabled() {
		t.Fatal("ANALYTICS_DISABLED=1 must yield !Enabled()")
	}
}

func TestCapture_PropagatesTenant(t *testing.T) {
	m := newMockServer(t)
	c := New(Config{
		APIKey:        "phc_test",
		Host:          m.srv.URL,
		Environment:   "test",
		ServiceName:   "r1-test",
		FlushAt:       1,
		FlushInterval: 50 * time.Millisecond,
		ShutdownWait:  2 * time.Second,
	})
	if !c.Enabled() {
		t.Fatal("client not enabled")
	}
	ctx := WithTenantID(context.Background(), "tenant-uuid-A")
	c.Capture(ctx, "user-1", "session_started", map[string]any{"mode": "interactive"})

	events := drainEvents(t, c, m)
	if len(events) == 0 {
		t.Fatalf("no events received; paths=%v", m.requestPath)
	}
	ev := events[0]
	props, _ := ev["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("missing properties in %v", ev)
	}
	if props["tenant_id"] != "tenant-uuid-A" {
		t.Errorf("tenant_id = %v, want tenant-uuid-A", props["tenant_id"])
	}
	groups, _ := props["$groups"].(map[string]any)
	if groups == nil {
		t.Fatalf("missing $groups in %v", props)
	}
	if groups["tenant"] != "tenant-uuid-A" {
		t.Errorf("$groups.tenant = %v, want tenant-uuid-A", groups["tenant"])
	}
}

func TestCapture_OmitsTenantWhenEmpty(t *testing.T) {
	m := newMockServer(t)
	c := New(Config{
		APIKey:        "phc_test",
		Host:          m.srv.URL,
		FlushAt:       1,
		FlushInterval: 50 * time.Millisecond,
		ShutdownWait:  2 * time.Second,
	})
	c.Capture(context.Background(), "user-1", "session_started", nil)
	events := drainEvents(t, c, m)
	if len(events) == 0 {
		t.Fatal("no events received")
	}
	props, _ := events[0]["properties"].(map[string]any)
	if _, has := props["tenant_id"]; has {
		t.Errorf("tenant_id should be absent when ctx has no tenant; got %v", props["tenant_id"])
	}
	if _, has := props["$groups"]; has {
		t.Errorf("$groups must be omitted when tenant is empty")
	}
}

func TestEventNameHygiene(t *testing.T) {
	for _, name := range AllEvents {
		if !EventNameHygiene.MatchString(string(name)) {
			t.Errorf("event %q fails hygiene regex", name)
		}
	}
	if len(AllEvents) != 24 {
		t.Errorf("AllEvents has %d entries, spec requires exactly 24", len(AllEvents))
	}
	known := make(map[EventName]bool, len(AllEvents))
	for _, n := range AllEvents {
		known[n] = true
	}
	for bt, an := range BusToAnalytics {
		if !known[an] {
			t.Errorf("BusToAnalytics[%q] = %q is not in AllEvents", bt, an)
		}
	}
}

func TestPolicyOptOut_DisablesEntirely(t *testing.T) {
	m := newMockServer(t)
	policy := AnalyticsPolicy{Disabled: true}
	cfg := PolicyOverlay(Config{
		APIKey: "phc_test",
		Host:   m.srv.URL,
	}, policy)
	c := New(cfg)
	if c.Enabled() {
		t.Fatal("policy disabled but client still enabled")
	}
	c.Capture(context.Background(), "u1", "session_started", nil)
	time.Sleep(150 * time.Millisecond)
	if got := m.count.Load(); got != 0 {
		t.Errorf("policy-disabled client emitted %d events", got)
	}
}

func TestTenantOptOut_DropsMatchingTenant(t *testing.T) {
	m := newMockServer(t)
	policy := AnalyticsPolicy{TenantOptOuts: []string{"tenant-opt-out"}}
	cfg := PolicyOverlay(Config{
		APIKey:        "phc_test",
		Host:          m.srv.URL,
		FlushAt:       1,
		FlushInterval: 50 * time.Millisecond,
		ShutdownWait:  2 * time.Second,
	}, policy)
	c := New(cfg)

	ctxOpted := WithTenantID(context.Background(), "tenant-opt-out")
	c.Capture(ctxOpted, "u1", "session_started", nil)

	ctxPass := WithTenantID(context.Background(), "tenant-allowed")
	c.Capture(ctxPass, "u2", "session_started", nil)

	events := drainEvents(t, c, m)
	sawAllowed := false
	for _, ev := range events {
		props, _ := ev["properties"].(map[string]any)
		if props["tenant_id"] == "tenant-opt-out" {
			t.Errorf("opted-out tenant leaked an event: %v", ev)
		}
		if props["tenant_id"] == "tenant-allowed" {
			sawAllowed = true
		}
	}
	if !sawAllowed {
		t.Fatal("allowed tenant produced no events")
	}
}

func TestLoadPolicy_MissingFile(t *testing.T) {
	p, err := LoadPolicy("/tmp/does-not-exist-r1-policy-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".yaml")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if p.Disabled || len(p.TenantOptOuts) != 0 {
		t.Errorf("missing file should yield zero policy, got %+v", p)
	}
}

func TestLoadPolicy_ParsesYAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/r1.policy.yaml"
	body := []byte("analytics:\n  disabled: false\n  tenant_optouts:\n    - t1\n    - t2\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Disabled {
		t.Error("expected disabled=false")
	}
	if len(p.TenantOptOuts) != 2 || p.TenantOptOuts[0] != "t1" {
		t.Errorf("tenant_optouts not parsed: %+v", p.TenantOptOuts)
	}
}

func TestContextLookup_RememberLookupForget(t *testing.T) {
	l := NewContextLookup(8)
	l.Remember("m1", "tenant-A")
	if got := l.Lookup("m1"); got != "tenant-A" {
		t.Errorf("Lookup = %q", got)
	}
	if l.Len() != 1 {
		t.Errorf("Len = %d, want 1", l.Len())
	}
	l.Forget("m1")
	if got := l.Lookup("m1"); got != "" {
		t.Errorf("after Forget Lookup = %q", got)
	}
}

func TestContextLookup_BoundedEviction(t *testing.T) {
	l := NewContextLookup(4)
	for i := 0; i < 10; i++ {
		l.Remember("m-"+strconv.Itoa(i), "tenant-"+strconv.Itoa(i))
	}
	if got := l.Len(); got > 4 {
		t.Errorf("Len after eviction = %d, want <=4", got)
	}
}

func TestRedact_StripsEmailAndAbsPath(t *testing.T) {
	c := New(Config{APIKey: "", Disabled: true})
	in := map[string]any{
		"email":   "alice@example.com",
		"path":    "/home/eric/repos/r1-agent/internal/secret",
		"safe":    "session_started",
		"counter": 42,
		"bearer":  "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123",
	}
	out := c.applyRedaction(in)
	if out["email"] != redactedValue {
		t.Errorf("email not redacted: %v", out["email"])
	}
	if out["path"] != redactedValue {
		t.Errorf("path not redacted: %v", out["path"])
	}
	if out["bearer"] != redactedValue {
		t.Errorf("bearer not redacted: %v", out["bearer"])
	}
	if out["safe"] != "session_started" {
		t.Errorf("safe value altered: %v", out["safe"])
	}
	if out["counter"] != 42 {
		t.Errorf("non-string value altered: %v", out["counter"])
	}
}

type fakeRedactor struct {
	redacted map[string]bool
}

func (f *fakeRedactor) IsRedacted(id string) (bool, error) {
	return f.redacted[id], nil
}

func TestRedact_LedgerCheckerSwapsRedactedNodeID(t *testing.T) {
	c := New(Config{APIKey: "", Disabled: true})
	c.WithRedactionChecker(&fakeRedactor{redacted: map[string]bool{"node-7": true}})
	in := map[string]any{
		"prompt_node_id": "node-7",
		"other_node_id":  "node-8",
	}
	out := c.applyRedaction(in)
	if out["prompt_node_id"] != redactedValue {
		t.Errorf("redacted node_id not blanked: %v", out["prompt_node_id"])
	}
	if out["other_node_id"] != "node-8" {
		t.Errorf("non-redacted node_id altered: %v", out["other_node_id"])
	}
}

func TestCapture_AllTwentyFourEventsRoundTrip(t *testing.T) {
	m := newMockServer(t)
	c := New(Config{
		APIKey:        "phc_test",
		Host:          m.srv.URL,
		FlushAt:       1,
		FlushInterval: 30 * time.Millisecond,
		ShutdownWait:  2 * time.Second,
	})
	ctx := WithTenantID(context.Background(), "tenant-rt")
	for _, name := range AllEvents {
		c.Capture(ctx, "user-rt", string(name), map[string]any{"k": "v"})
	}
	events := drainEvents(t, c, m)
	got := make(map[string]bool, len(events))
	for _, ev := range events {
		if name, ok := ev["event"].(string); ok {
			got[name] = true
		}
	}
	for _, name := range AllEvents {
		if !got[string(name)] {
			t.Errorf("event %q did not reach the mock server", name)
		}
	}
}

func TestTenantIDFromContext_NilSafe(t *testing.T) {
	if got := TenantIDFromContext(nil); got != "" {
		t.Errorf("nil ctx tenant = %q, want empty", got)
	}
	ctx := WithTenantID(context.Background(), "t-x")
	if got := TenantIDFromContext(ctx); got != "t-x" {
		t.Errorf("round-trip tenant = %q", got)
	}
	if got := WithTenantID(context.Background(), ""); got == nil {
		t.Error("WithTenantID with empty tenantID should return non-nil ctx")
	}
}
