package a2a

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, token string) *Server {
	t.Helper()
	card := Build(Options{
		Name:    "test-agent",
		Version: "1.0.0",
		URL:     "http://localhost:0",
		Capabilities: []CapabilityRef{
			{Name: "foo", Version: "1.0.0"},
		},
	})
	return NewServer(card, NewInMemoryTaskStore(), token)
}

func TestHTTPServer_ServesAgentCard(t *testing.T) {
	srv := newTestServer(t, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/.well-known/agent.json")
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q", ct)
	}
	var card AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if card.Name != "test-agent" {
		t.Errorf("name=%q", card.Name)
	}
	if card.ProtocolVersion == "" {
		t.Errorf("protocol version empty")
	}
	if len(card.Capabilities) != 1 || card.Capabilities[0].Name != "foo" {
		t.Errorf("capabilities=%+v", card.Capabilities)
	}
}

func TestHTTPServer_CardSurvivesLiveSwap(t *testing.T) {
	srv := newTestServer(t, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	newCard := Build(Options{Name: "renamed", Version: "2.0.0"})
	srv.SetCard(newCard)

	resp, err := http.Get(ts.URL + "/.well-known/agent.json")
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	defer resp.Body.Close()
	var card AgentCard
	_ = json.NewDecoder(resp.Body).Decode(&card)
	if card.Name != "renamed" {
		t.Errorf("post-swap name=%q want renamed", card.Name)
	}
}

func TestHTTPServer_CardOnlyGET(t *testing.T) {
	srv := newTestServer(t, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/.well-known/agent.json", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Errorf("status=%d want 405", resp.StatusCode)
	}
}

func TestHTTPServer_Health(t *testing.T) {
	srv := newTestServer(t, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d", resp.StatusCode)
	}
}

func TestHTTPServer_RPCSubmitWorks(t *testing.T) {
	srv := newTestServer(t, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"a2a.task.submit","params":{"prompt":{"task":"do a thing"}}}`)
	resp, err := http.Post(ts.URL+"/a2a/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post rpc: %v", err)
	}
	defer resp.Body.Close()
	var r struct {
		Result *Task            `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Error != nil {
		t.Fatalf("unexpected error: %s", string(*r.Error))
	}
	if r.Result == nil || r.Result.ID == "" {
		t.Errorf("no task in result: %+v", r)
	}
	if r.Result.Status != TaskSubmitted {
		t.Errorf("status=%v want submitted", r.Result.Status)
	}
}

func TestHTTPServer_RPCRejectsBadJSON(t *testing.T) {
	srv := newTestServer(t, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/a2a/rpc", "application/json", strings.NewReader(`{not json`))
	if err != nil {
		t.Fatalf("post rpc: %v", err)
	}
	defer resp.Body.Close()
	var r struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)
	if r.Error == nil || r.Error.Code != RPCParseError {
		t.Errorf("expected parse error, got %+v", r.Error)
	}
}

func TestHTTPServer_RPCRejectsUnknownMethod(t *testing.T) {
	srv := newTestServer(t, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"a2a.task.fabricate","params":{}}`)
	resp, err := http.Post(ts.URL+"/a2a/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var r struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)
	if r.Error == nil || r.Error.Code != RPCMethodNotFound {
		t.Errorf("expected method-not-found, got %+v", r.Error)
	}
}

func TestHTTPServer_RPCAuthRequiredWhenTokenSet(t *testing.T) {
	srv := newTestServer(t, "correct-token")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Without bearer → unauthorized.
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"a2a.task.submit","params":{"prompt":{}}}`)
	resp, err := http.Post(ts.URL+"/a2a/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var r struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)
	if r.Error == nil || r.Error.Code != RPCUnauthorized {
		t.Errorf("expected unauthorized, got %+v", r.Error)
	}
}

func TestHTTPServer_RPCAcceptsBearer(t *testing.T) {
	srv := newTestServer(t, "correct-token")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"a2a.task.submit","params":{"prompt":{}}}`)
	req, _ := http.NewRequest("POST", ts.URL+"/a2a/rpc", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer correct-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var r struct {
		Result *Task `json:"result"`
		Error  any   `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)
	if r.Error != nil {
		t.Fatalf("error: %+v", r.Error)
	}
	if r.Result == nil {
		t.Fatal("no result with valid bearer")
	}
}

func TestHTTPServer_CardOpenEvenWithAuth(t *testing.T) {
	// Agent Card must ALWAYS be accessible — auth only
	// gates /a2a/rpc.
	srv := newTestServer(t, "some-token")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/.well-known/agent.json")
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d: card must be open", resp.StatusCode)
	}
}

func TestHTTPServer_SubmitThenStatus(t *testing.T) {
	srv := newTestServer(t, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Submit.
	submitBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"a2a.task.submit","params":{"prompt":{"x":1}}}`)
	resp, _ := http.Post(ts.URL+"/a2a/rpc", "application/json", bytes.NewReader(submitBody))
	var sr struct {
		Result Task `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&sr)
	resp.Body.Close()
	if sr.Result.ID == "" {
		t.Fatal("no task id returned")
	}

	// Status.
	statusBody := []byte(`{"jsonrpc":"2.0","id":2,"method":"a2a.task.status","params":{"taskId":"` + sr.Result.ID + `"}}`)
	resp2, _ := http.Post(ts.URL+"/a2a/rpc", "application/json", bytes.NewReader(statusBody))
	var ssr struct {
		Result Task `json:"result"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&ssr)
	resp2.Body.Close()
	if ssr.Result.ID != sr.Result.ID {
		t.Errorf("status returned wrong task: %v vs %v", ssr.Result.ID, sr.Result.ID)
	}
}
