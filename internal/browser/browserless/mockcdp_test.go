// mockcdp_test.go: minimal CDP-over-WS test server shared by all the
// Browserless client tests. The same shape is reused by the inhouse
// package's tests via a copy (the shared cdpmock extraction the spec
// names happens only when duplication exceeds ~150 LOC).

package browserless

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

// mockCDP is a tiny in-process CDP-over-WS server. It speaks just
// enough of the protocol to satisfy the conformance suite:
// Target.createBrowserContext / Target.createTarget /
// Target.attachToTarget / Target.disposeBrowserContext /
// Target.sendMessageToTarget (with Runtime.evaluate +
// Page.navigate + Page.captureScreenshot inner forwarding).
type mockCDP struct {
	server      *httptest.Server
	wsURL       string
	ctxCounter  atomic.Int64
	targetCount atomic.Int64
	sessCount   atomic.Int64

	mu             sync.Mutex
	contextCookies map[string][]string // browserContextID → cookies set
	requireToken   string              // when non-empty, reject handshakes missing it
	rejectAuth     atomic.Bool
}

// startMockCDP boots an httptest.NewServer that upgrades to WS and
// dispatches CDP frames.
func startMockCDP(t *testing.T) *mockCDP {
	t.Helper()
	m := &mockCDP{contextCookies: make(map[string][]string)}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	u, _ := url.Parse(m.server.URL)
	u.Scheme = "ws"
	m.wsURL = u.String()
	t.Cleanup(func() { m.server.Close() })
	return m
}

func (m *mockCDP) handle(w http.ResponseWriter, r *http.Request) {
	if m.rejectAuth.Load() {
		http.Error(w, "auth rejected", http.StatusUnauthorized)
		return
	}
	if m.requireToken != "" {
		if r.URL.Query().Get("token") != m.requireToken {
			http.Error(w, "auth rejected", http.StatusUnauthorized)
			return
		}
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	ctx := r.Context()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var f struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		resp := m.dispatch(f.Method, f.Params)
		out := struct {
			ID     int64       `json:"id"`
			Result interface{} `json:"result,omitempty"`
		}{ID: f.ID, Result: resp}
		buf, _ := json.Marshal(out)
		if err := c.Write(ctx, websocket.MessageText, buf); err != nil {
			return
		}
	}
}

// dispatch returns the result JSON for a CDP method call. Always
// returns SOMETHING — never errors — so the client's Send() path
// completes deterministically.
func (m *mockCDP) dispatch(method string, params json.RawMessage) any {
	switch method {
	case "Target.createBrowserContext":
		id := "ctx-" + intToStr(m.ctxCounter.Add(1))
		m.mu.Lock()
		m.contextCookies[id] = nil
		m.mu.Unlock()
		return map[string]any{"browserContextId": id}
	case "Target.createTarget":
		id := "tgt-" + intToStr(m.targetCount.Add(1))
		return map[string]any{"targetId": id}
	case "Target.attachToTarget":
		id := "sess-" + intToStr(m.sessCount.Add(1))
		return map[string]any{"sessionId": id}
	case "Target.disposeBrowserContext":
		return map[string]any{}
	case "Target.sendMessageToTarget":
		var p struct {
			SessionID string `json:"sessionId"`
			Message   string `json:"message"`
		}
		_ = json.Unmarshal(params, &p)
		// Forward inner method.
		var inner struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal([]byte(p.Message), &inner)
		innerResp := m.dispatchInner(inner.Method, inner.Params)
		out := map[string]any{"id": inner.ID, "result": innerResp}
		buf, _ := json.Marshal(out)
		return map[string]any{"message": string(buf)}
	case "Network.setRequestInterception":
		return map[string]any{}
	case "Network.continueInterceptedRequest":
		return map[string]any{}
	}
	return map[string]any{}
}

// dispatchInner handles methods inside the Target.sendMessageToTarget
// envelope. The Browserless WS uses this for per-target routing.
func (m *mockCDP) dispatchInner(method string, params json.RawMessage) any {
	switch method {
	case "Page.navigate":
		var p struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(params, &p)
		return map[string]any{"frameId": "f1", "loaderId": "l1"}
	case "Runtime.evaluate":
		var p struct {
			Expression string `json:"expression"`
		}
		_ = json.Unmarshal(params, &p)
		// Recognize the conformance suite's well-known shapes.
		// location.href → a synthetic URL string.
		if p.Expression == "location.href" {
			return map[string]any{"result": map[string]any{"type": "string", "value": "https://mock/example"}}
		}
		// querySelector existence checks → always "false" so the
		// conformance WaitFor-missing assertion times out cleanly.
		if containsSubstr(p.Expression, "document.querySelector") {
			return map[string]any{"result": map[string]any{"type": "boolean", "value": false}}
		}
		// Arithmetic: very small evaluator for the assertion
		// `Eval("1+2") → 3` against a mock that doesn't run JS.
		if v, ok := tinyArith(p.Expression); ok {
			return map[string]any{"result": map[string]any{"type": "number", "value": v}}
		}
		return map[string]any{"result": map[string]any{"type": "undefined"}}
	case "Page.captureScreenshot":
		return map[string]any{"data": pngBase64()}
	}
	return map[string]any{}
}

// pngBase64 returns base64(PNG-magic + 4 bytes) — enough to verify
// the screenshot conformance assertion.
func pngBase64() string {
	// "iVBORw0KGgoAAAA=" = base64 of the PNG magic (8 bytes).
	return "iVBORw0KGgoAAAA="
}

// containsSubstr is a tiny substring check; the test pkg avoids
// pulling strings into every call site for cleanliness.
func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// tinyArith recognises "<int><op><int>" with op in +-*/ and returns
// the numeric result.
func tinyArith(s string) (int, bool) {
	clean := stripWS(s)
	for _, op := range []byte{'+', '-', '*', '/'} {
		for i := 1; i < len(clean)-1; i++ {
			if clean[i] != op {
				continue
			}
			a, aOK := atoi(clean[:i])
			b, bOK := atoi(clean[i+1:])
			if !aOK || !bOK {
				continue
			}
			switch op {
			case '+':
				return a + b, true
			case '-':
				return a - b, true
			case '*':
				return a * b, true
			case '/':
				if b == 0 {
					return 0, true
				}
				return a / b, true
			}
		}
	}
	return 0, false
}

func stripWS(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	}
	n := 0
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n, true
}

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Ensure the package compiles even if the underlying CDP layer
// changes — these are sanity refs.
var (
	_ = context.Background
	_ = atomic.Int64{}
)

// TestMockCDP_SanityHelpers asserts the small helpers used by the
// mock server. The mock itself is exercised via every other test in
// the package; this test guards the inlined arithmetic + atoi paths
// the mock uses to fulfil Runtime.evaluate without a JS engine.
func TestMockCDP_SanityHelpers(t *testing.T) {
	t.Parallel()
	if v, ok := tinyArith("1+2"); !ok || v != 3 {
		t.Errorf("tinyArith(1+2) = %d ok=%v, want 3 true", v, ok)
	}
	if v, ok := tinyArith("10-4"); !ok || v != 6 {
		t.Errorf("tinyArith(10-4) = %d ok=%v, want 6 true", v, ok)
	}
	if v, ok := tinyArith("not math"); ok {
		t.Errorf("tinyArith(not math) = %d ok=%v, want false", v, ok)
	}
	if n, ok := atoi("123"); !ok || n != 123 {
		t.Errorf("atoi(123) = %d ok=%v", n, ok)
	}
	if n, ok := atoi("-7"); !ok || n != -7 {
		t.Errorf("atoi(-7) = %d ok=%v", n, ok)
	}
	if _, ok := atoi("1x"); ok {
		t.Errorf("atoi(1x) should fail")
	}
	if intToStr(0) != "0" {
		t.Errorf("intToStr(0) = %q", intToStr(0))
	}
	if intToStr(42) != "42" {
		t.Errorf("intToStr(42) = %q", intToStr(42))
	}
	if intToStr(-3) != "-3" {
		t.Errorf("intToStr(-3) = %q", intToStr(-3))
	}
	if !containsSubstr("hello world", "wor") {
		t.Errorf("containsSubstr should find substring")
	}
	if containsSubstr("abc", "xyz") {
		t.Errorf("containsSubstr should not find absent substring")
	}
	if stripWS(" a b\tc ") != "abc" {
		t.Errorf("stripWS dropped wrong chars: %q", stripWS(" a b\tc "))
	}
}
