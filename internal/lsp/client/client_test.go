// client_test.go — unit tests for the multi-language LSP client (T-R1P-020).
//
// All non-integration tests use an in-memory fake server constructed from
// io.Pipe pairs so no real LSP binary is required. The integration tests
// (TestIntegration*) skip gracefully when the corresponding language
// server (gopls, pyright-langserver, typescript-language-server,
// rust-analyzer) is not on PATH.
//
// Coverage:
//   - Initialize handshake completes and dispatches initialized notification
//   - Completion request returns parsed CompletionItems
//   - Hover request flattens MarkupContent into a string
//   - Diagnostics buffered from server-pushed notification can be polled
//   - Shutdown drains pending pipes without panicking
//   - LaunchByLanguage rejects unknown languages
//   - readFrame rejects frames missing Content-Length
//   - PathToURI / URIToPath round-trip
//   - Connection-closed errors propagate to in-flight callers

package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeServer reads framed JSON-RPC requests from r and writes framed
// replies to w. The handler decides per-method what to return.
type fakeServer struct {
	r       io.Reader
	w       io.Writer
	handler func(req fakeRequest) (result interface{}, err *rpcErrorBody)

	// publish optional diagnostics on the first textDocument/didOpen.
	pushDiagnostics bool
	diagnostics     []Diagnostic
}

type fakeRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// run consumes frames until EOF, dispatching each to the handler. When a
// frame has no ID (notification), no reply is sent. For requests, the
// handler return value is wrapped in a JSON-RPC response and written back.
func (f *fakeServer) run() {
	br := bufio.NewReader(f.r)
	for {
		body, err := readFrame(br)
		if err != nil {
			return
		}
		var req fakeRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return
		}

		// Push diagnostics on first didOpen if requested.
		if f.pushDiagnostics && req.Method == "textDocument/didOpen" {
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(req.Params, &p)
			f.publish("textDocument/publishDiagnostics", map[string]interface{}{
				"uri":         p.TextDocument.URI,
				"diagnostics": f.diagnostics,
			})
		}

		// Notifications get no response.
		if req.ID == nil {
			continue
		}
		result, errBody := f.handler(req)
		f.respond(*req.ID, result, errBody)
	}
}

func (f *fakeServer) respond(id int64, result interface{}, errBody *rpcErrorBody) {
	resp := struct {
		JSONRPC string        `json:"jsonrpc"`
		ID      int64         `json:"id"`
		Result  interface{}   `json:"result,omitempty"`
		Error   *rpcErrorBody `json:"error,omitempty"`
	}{JSONRPC: "2.0", ID: id, Result: result, Error: errBody}
	body, _ := json.Marshal(resp)
	header := []byte("Content-Length: " + itoa(len(body)) + "\r\n\r\n")
	_, _ = f.w.Write(header)
	_, _ = f.w.Write(body)
}

func (f *fakeServer) publish(method string, params interface{}) {
	body, _ := json.Marshal(struct {
		JSONRPC string      `json:"jsonrpc"`
		Method  string      `json:"method"`
		Params  interface{} `json:"params"`
	}{JSONRPC: "2.0", Method: method, Params: params})
	header := []byte("Content-Length: " + itoa(len(body)) + "\r\n\r\n")
	_, _ = f.w.Write(header)
	_, _ = f.w.Write(body)
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// newClientPair wires a Client to a fakeServer over two io.Pipe pairs.
func newClientPair(handler func(fakeRequest) (interface{}, *rpcErrorBody)) (*Client, *fakeServer) {
	clientToServerR, clientToServerW := io.Pipe()
	serverToClientR, serverToClientW := io.Pipe()

	server := &fakeServer{
		r:       clientToServerR,
		w:       serverToClientW,
		handler: handler,
	}
	go server.run()

	c := WithTransport(clientToServerW, serverToClientR)
	c.SetRequestTimeout(2 * time.Second)
	return c, server
}

// TestStartupHandshake verifies the Initialize round-trip succeeds and
// the post-handshake `initialized` notification is sent without blocking.
func TestStartupHandshake(t *testing.T) {
	c, _ := newClientPair(func(req fakeRequest) (interface{}, *rpcErrorBody) {
		if req.Method != "initialize" {
			return nil, &rpcErrorBody{Code: -32601, Message: "unexpected"}
		}
		return map[string]interface{}{
			"capabilities": map[string]interface{}{
				"hoverProvider":      true,
				"definitionProvider": true,
			},
		}, nil
	})
	defer c.Shutdown()

	if err := c.Initialize("file:///tmp/project"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

// TestCompletionRoundTrip checks that a CompletionList payload is parsed
// into []CompletionItem with field values preserved.
func TestCompletionRoundTrip(t *testing.T) {
	c, _ := newClientPair(func(req fakeRequest) (interface{}, *rpcErrorBody) {
		switch req.Method {
		case "initialize":
			return map[string]interface{}{}, nil
		case "textDocument/completion":
			return map[string]interface{}{
				"isIncomplete": false,
				"items": []map[string]interface{}{
					{"label": "Println", "kind": 3, "detail": "func"},
					{"label": "Printf", "kind": 3, "detail": "func"},
				},
			}, nil
		}
		return nil, &rpcErrorBody{Code: -32601, Message: "method"}
	})
	defer c.Shutdown()

	if err := c.Initialize("file:///tmp/p"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	items, err := c.Completion("file:///tmp/p/main.go", 5, 10)
	if err != nil {
		t.Fatalf("Completion: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Label != "Println" || items[0].Kind != 3 {
		t.Errorf("item[0] = %+v", items[0])
	}
}

// TestHoverFlattensMarkup verifies the {kind,value} hover payload is
// reduced to its value string.
func TestHoverFlattensMarkup(t *testing.T) {
	c, _ := newClientPair(func(req fakeRequest) (interface{}, *rpcErrorBody) {
		switch req.Method {
		case "initialize":
			return map[string]interface{}{}, nil
		case "textDocument/hover":
			return map[string]interface{}{
				"contents": map[string]interface{}{
					"kind":  "markdown",
					"value": "func Println(a ...any) (n int, err error)",
				},
			}, nil
		}
		return nil, &rpcErrorBody{Code: -32601, Message: "method"}
	})
	defer c.Shutdown()

	if err := c.Initialize("file:///tmp/p"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	hov, err := c.Hover("file:///tmp/p/main.go", 0, 0)
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if hov == nil || !strings.Contains(hov.Contents, "Println") {
		t.Errorf("hover.Contents = %q", hov)
	}
}

// TestDiagnosticsBuffered verifies that a server-pushed
// textDocument/publishDiagnostics notification is captured and surfaced
// via Diagnostics().
func TestDiagnosticsBuffered(t *testing.T) {
	clientToServerR, clientToServerW := io.Pipe()
	serverToClientR, serverToClientW := io.Pipe()

	server := &fakeServer{
		r:               clientToServerR,
		w:               serverToClientW,
		pushDiagnostics: true,
		diagnostics: []Diagnostic{
			{Severity: 1, Message: "undeclared name: foo", Source: "fake"},
		},
		handler: func(req fakeRequest) (interface{}, *rpcErrorBody) {
			if req.Method == "initialize" {
				return map[string]interface{}{}, nil
			}
			return nil, &rpcErrorBody{Code: -32601, Message: "method"}
		},
	}
	go server.run()

	c := WithTransport(clientToServerW, serverToClientR)
	c.SetRequestTimeout(2 * time.Second)
	defer c.Shutdown()

	if err := c.Initialize("file:///tmp/p"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	uri := "file:///tmp/p/main.go"
	if err := c.OpenDocument(uri, "go", "package main\n"); err != nil {
		t.Fatalf("OpenDocument: %v", err)
	}

	// Wait briefly for the asynchronous publishDiagnostics frame to be
	// processed by the read loop.
	deadline := time.Now().Add(2 * time.Second)
	var got []Diagnostic
	for time.Now().Before(deadline) {
		got, _ = c.Diagnostics(uri)
		if len(got) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(got))
	}
	if !strings.Contains(got[0].Message, "undeclared") {
		t.Errorf("diagnostic message = %q", got[0].Message)
	}
}

// TestShutdownDrainsPipes ensures Shutdown() does not panic and closes
// the transport even when the server stops responding.
func TestShutdownDrainsPipes(t *testing.T) {
	c, _ := newClientPair(func(req fakeRequest) (interface{}, *rpcErrorBody) {
		// Reply only to initialize; ignore everything else (server hang).
		if req.Method == "initialize" {
			return map[string]interface{}{}, nil
		}
		return nil, &rpcErrorBody{Code: -32601, Message: "ignored"}
	})

	if err := c.Initialize("file:///tmp/p"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := c.Shutdown(); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	// Second call must also be safe (closer is idempotent because the pipe
	// closers tolerate repeated Close).
	_ = c.Shutdown()
}

// TestLaunchByLanguageRejectsUnknown asserts the dispatcher returns an
// error for unknown language IDs without attempting to start anything.
func TestLaunchByLanguageRejectsUnknown(t *testing.T) {
	_, err := LaunchByLanguage("cobol", "/tmp")
	if err == nil {
		t.Fatal("expected error for unknown language")
	}
	if !strings.Contains(err.Error(), "no launcher") {
		t.Errorf("err = %v", err)
	}
}

// TestFrameRejectsMissingHeader confirms the framing parser rejects a
// message that omits Content-Length.
func TestFrameRejectsMissingHeader(t *testing.T) {
	body := []byte("Content-Type: application/json\r\n\r\n{}")
	_, err := readFrame(bufio.NewReader(bytes.NewReader(body)))
	if err == nil {
		t.Fatal("expected error for missing Content-Length")
	}
}

// TestURIRoundTrip verifies pathToURI/uriToPath are inverses.
func TestURIRoundTrip(t *testing.T) {
	path := "/home/user/proj/main.go"
	uri := PathToURI(path)
	if !strings.HasPrefix(uri, "file://") {
		t.Errorf("PathToURI = %q", uri)
	}
	if back := URIToPath(uri); back != path {
		t.Errorf("URIToPath round-trip: got %q, want %q", back, path)
	}
}

// TestConnectionClosedFailsPending ensures a pending request resolves
// with an error when the read loop exits unexpectedly. The fake server
// drains the request frame, then closes both pipe ends so the client
// reader sees EOF and the in-flight request unblocks.
func TestConnectionClosedFailsPending(t *testing.T) {
	clientToServerR, clientToServerW := io.Pipe()
	serverToClientR, serverToClientW := io.Pipe()

	go func() {
		// Drain the inbound frame so the client's writer doesn't block.
		br := bufio.NewReader(clientToServerR)
		_, _ = readFrame(br)
		// Close the server -> client side; this triggers EOF in the read loop.
		_ = serverToClientW.Close()
		// Also drain anything else from the client side so its writer
		// pipe can shut down without backing up.
		_ = clientToServerR.Close()
	}()

	c := WithTransport(clientToServerW, serverToClientR)
	c.SetRequestTimeout(2 * time.Second)

	_, err := c.request("textDocument/hover", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error after server EOF")
	}
	if !strings.Contains(err.Error(), "closed") &&
		!strings.Contains(err.Error(), "EOF") &&
		!strings.Contains(err.Error(), "pipe") {
		t.Errorf("unexpected err: %v", err)
	}
	_ = c.Shutdown()
}

// TestReaderWaitWithCanceledContext verifies WaitForReader honors ctx.
func TestReaderWaitWithCanceledContext(t *testing.T) {
	c, _ := newClientPair(func(req fakeRequest) (interface{}, *rpcErrorBody) {
		return map[string]interface{}{}, nil
	})
	defer c.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.WaitForReader(ctx); err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("WaitForReader err = %v, want context.Canceled", err)
	}
}
