// mockcdp_test.go: mock CDP-over-WS server for the inhouse client.
// Mirrors the Browserless package mock; spec C6 §T13 calls out
// extracting a shared testutil/cdpmock package if duplication
// exceeds ~150 LOC. Today each package keeps its own copy because
// the mocks diverge: this one asserts on the Authorization header
// (Bearer token), the Browserless one asserts on the token query
// parameter.

package inhouse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

type mockCDP struct {
	server      *httptest.Server
	wsURL       string
	ctxCounter  atomic.Int64
	targetCount atomic.Int64
	sessCount   atomic.Int64

	requireBearer string         // when non-empty, reject missing/wrong Authorization
	receivedAuth  atomic.Pointer[string]
}

func startMockCDP(t *testing.T) *mockCDP {
	t.Helper()
	m := &mockCDP{}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	u, _ := url.Parse(m.server.URL)
	u.Scheme = "ws"
	m.wsURL = u.String()
	t.Cleanup(func() { m.server.Close() })
	return m
}

func (m *mockCDP) handle(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	m.receivedAuth.Store(&auth)
	if m.requireBearer != "" && auth != "Bearer "+m.requireBearer {
		http.Error(w, "auth rejected", http.StatusUnauthorized)
		return
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

func (m *mockCDP) dispatch(method string, params json.RawMessage) any {
	switch method {
	case "Target.createBrowserContext":
		return map[string]any{"browserContextId": "ctx-" + i64ToStr(m.ctxCounter.Add(1))}
	case "Target.createTarget":
		return map[string]any{"targetId": "tgt-" + i64ToStr(m.targetCount.Add(1))}
	case "Target.attachToTarget":
		return map[string]any{"sessionId": "sess-" + i64ToStr(m.sessCount.Add(1))}
	case "Target.disposeBrowserContext":
		return map[string]any{}
	case "Target.sendMessageToTarget":
		var p struct {
			SessionID string `json:"sessionId"`
			Message   string `json:"message"`
		}
		_ = json.Unmarshal(params, &p)
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
	}
	return map[string]any{}
}

func (m *mockCDP) dispatchInner(method string, params json.RawMessage) any {
	switch method {
	case "Page.navigate":
		return map[string]any{"frameId": "f1", "loaderId": "l1"}
	case "Runtime.evaluate":
		var p struct {
			Expression string `json:"expression"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Expression == "location.href" {
			return map[string]any{"result": map[string]any{"type": "string", "value": "https://browser.r1.run/example"}}
		}
		if strings.Contains(p.Expression, "document.querySelector") {
			return map[string]any{"result": map[string]any{"type": "boolean", "value": false}}
		}
		if v, ok := smallArith(p.Expression); ok {
			return map[string]any{"result": map[string]any{"type": "number", "value": v}}
		}
		return map[string]any{"result": map[string]any{"type": "undefined"}}
	case "Page.captureScreenshot":
		return map[string]any{"data": "iVBORw0KGgoAAAA="}
	}
	return map[string]any{}
}

// smallArith recognises "<int><op><int>"; mirrors the Browserless
// mock's tinyArith.
func smallArith(s string) (int, bool) {
	clean := stripSpaceTabs(s)
	for _, op := range []byte{'+', '-', '*', '/'} {
		for i := 1; i < len(clean)-1; i++ {
			if clean[i] != op {
				continue
			}
			a, aOK := decInt(clean[:i])
			b, bOK := decInt(clean[i+1:])
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

func stripSpaceTabs(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

func decInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg, i = true, 1
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

func i64ToStr(n int64) string {
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

// TestMockCDP_HelpersSanity exercises the inlined helpers (Go
// requires non-trivial tests to cover them; the mock itself is
// exercised by every other test in this package).
func TestMockCDP_HelpersSanity(t *testing.T) {
	t.Parallel()
	if v, ok := smallArith("3+4"); !ok || v != 7 {
		t.Errorf("smallArith(3+4) = %d ok=%v", v, ok)
	}
	if v, ok := smallArith("nope"); ok {
		t.Errorf("smallArith(nope) = %d ok=%v, want false", v, ok)
	}
	if v, ok := decInt("123"); !ok || v != 123 {
		t.Errorf("decInt(123) = %d ok=%v", v, ok)
	}
	if i64ToStr(0) != "0" || i64ToStr(42) != "42" || i64ToStr(-3) != "-3" {
		t.Errorf("i64ToStr: %q %q %q", i64ToStr(0), i64ToStr(42), i64ToStr(-3))
	}
	if stripSpaceTabs(" a b\tc ") != "abc" {
		t.Errorf("stripSpaceTabs: %q", stripSpaceTabs(" a b\tc "))
	}
}

// Sanity refs to silence unused-import warnings if the mock changes.
var (
	_ = context.Background
	_ = atomic.Int64{}
)
