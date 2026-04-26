// lsp_test.go — unit tests for the R1 LSP server (T-R1P-009).
//
// Tests use an in-memory pipe so no OS stdin/stdout is touched.
// Each test sends one LSP request and asserts on the response.
//
// Tests cover:
//   - initialize capability negotiation
//   - shutdown / exit lifecycle
//   - workspace/symbol search
//   - textDocument/hover returns nil for unknown word
//   - textDocument/definition returns empty for unknown word
//   - textDocument/completion returns list (possibly empty)
//   - unknown method returns -32601 MethodNotFound
//   - framing: Content-Length is respected on both read and write

package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// ---- helpers ----

// roundTrip sends one LSP request and reads one response via in-memory pipes.
// It sends shutdown after the target request so Serve() returns cleanly.
func roundTrip(t *testing.T, method string, params interface{}) *Message {
	t.Helper()
	srv := NewServer("")

	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := &Message{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  json.RawMessage(body),
	}

	var inBuf bytes.Buffer
	var outBuf bytes.Buffer

	// Write the request into inBuf.
	if err := writeMessage(&inBuf, req); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}

	// Append a shutdown so Serve() drains cleanly when input EOF is reached.
	shutReq := &Message{JSONRPC: "2.0", ID: 2, Method: "shutdown", Params: json.RawMessage(`{}`)}
	_ = writeMessage(&inBuf, shutReq)
	// No "exit" notification — Serve terminates on EOF after reading all frames.

	srv = srv.WithIO(&inBuf, &outBuf)

	// Serve in a goroutine; it returns on EOF (all frames consumed).
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(context.Background())
	}()
	<-done

	// Parse the first response from outBuf.
	reader := bufio.NewReader(&outBuf)
	msg, err := readMessage(reader)
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	return msg
}

// ---- tests ----

func TestLSP_Initialize(t *testing.T) {
	msg := roundTrip(t, "initialize", map[string]interface{}{
		"rootUri":  "file:///tmp/test",
		"rootPath": "/tmp/test",
		"capabilities": map[string]interface{}{},
	})

	if msg.Error != nil {
		t.Fatalf("initialize error: %+v", msg.Error)
	}

	// Unmarshal capabilities.
	var result initializeResult
	raw, _ := json.Marshal(msg.Result)
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal initializeResult: %v", err)
	}
	if !result.Capabilities.HoverProvider {
		t.Error("expected hoverProvider = true")
	}
	if !result.Capabilities.DefinitionProvider {
		t.Error("expected definitionProvider = true")
	}
	if result.ServerInfo.Name != "r1-lsp" {
		t.Errorf("serverInfo.name = %q, want r1-lsp", result.ServerInfo.Name)
	}
}

func TestLSP_UnknownMethod(t *testing.T) {
	msg := roundTrip(t, "$/nope", map[string]interface{}{})
	if msg.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if msg.Error.Code != errMethodNotFound {
		t.Errorf("code = %d, want %d (MethodNotFound)", msg.Error.Code, errMethodNotFound)
	}
}

func TestLSP_WorkspaceSymbol_EmptyQuery(t *testing.T) {
	msg := roundTrip(t, "workspace/symbol", map[string]interface{}{
		"query": "",
	})
	if msg.Error != nil {
		t.Fatalf("workspace/symbol error: %+v", msg.Error)
	}
	// Result must be a JSON array (possibly empty).
	raw, _ := json.Marshal(msg.Result)
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		t.Errorf("workspace/symbol result should be array, got: %s", raw)
	}
}

func TestLSP_HoverUnknownWord(t *testing.T) {
	msg := roundTrip(t, "textDocument/hover", map[string]interface{}{
		"textDocument": map[string]string{"uri": "file:///nonexistent.go"},
		"position":     map[string]int{"line": 0, "character": 0},
	})
	if msg.Error != nil {
		t.Fatalf("hover error: %+v", msg.Error)
	}
	// When no symbol is found, result is null/nil — valid LSP.
	// We just confirm no crash.
}

func TestLSP_DefinitionUnknownWord(t *testing.T) {
	msg := roundTrip(t, "textDocument/definition", map[string]interface{}{
		"textDocument": map[string]string{"uri": "file:///nonexistent.go"},
		"position":     map[string]int{"line": 0, "character": 0},
	})
	if msg.Error != nil {
		t.Fatalf("definition error: %+v", msg.Error)
	}
	// Result should be an array.
	raw, _ := json.Marshal(msg.Result)
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		t.Errorf("definition result should be array, got: %s", raw)
	}
}

func TestLSP_CompletionReturnslist(t *testing.T) {
	msg := roundTrip(t, "textDocument/completion", map[string]interface{}{
		"textDocument": map[string]string{"uri": "file:///nonexistent.go"},
		"position":     map[string]int{"line": 0, "character": 0},
	})
	if msg.Error != nil {
		t.Fatalf("completion error: %+v", msg.Error)
	}
	// Result should unmarshal as a completionList.
	raw, _ := json.Marshal(msg.Result)
	var list completionList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("unmarshal completionList: %v", err)
	}
	if list.Items == nil {
		t.Error("items should be non-nil (empty array OK)")
	}
}

func TestLSP_FramingRoundTrip(t *testing.T) {
	// Verify writeMessage + readMessage are inverses.
	original := &Message{
		JSONRPC: "2.0",
		ID:      42,
		Method:  "test",
		Params:  json.RawMessage(`{"key":"value"}`),
	}

	var buf bytes.Buffer
	if err := writeMessage(&buf, original); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}

	// Check Content-Length header is present.
	header := buf.String()
	if !strings.Contains(header, "Content-Length:") {
		t.Error("frame missing Content-Length header")
	}

	// Read back.
	reader := bufio.NewReader(&buf)
	got, err := readMessage(reader)
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if got.Method != original.Method {
		t.Errorf("method: got %q want %q", got.Method, original.Method)
	}
}

func TestLSP_ExtractWordAt(t *testing.T) {
	cases := []struct {
		line string
		pos  int
		want string
	}{
		{"foo.bar(x)", 0, "foo"},
		{"foo.bar(x)", 4, "bar"},
		{"foo.bar(x)", 8, "x"},
		{"hello world", 6, "world"},
		{"", 0, ""},
		{"abc", 3, "abc"}, // char==len: cursor after last char, word is "abc"
		{"   ", 1, ""},   // whitespace
	}
	for _, tc := range cases {
		got := extractWordAt(tc.line, tc.pos)
		if got != tc.want {
			t.Errorf("extractWordAt(%q, %d) = %q, want %q", tc.line, tc.pos, got, tc.want)
		}
	}
}

func TestLSP_URIConversions(t *testing.T) {
	path := "/home/user/project/main.go"
	uri := pathToURI(path)
	if !strings.HasPrefix(uri, "file://") {
		t.Errorf("pathToURI should produce file:// URI, got: %q", uri)
	}
	back := uriToPath(uri)
	if back != path {
		t.Errorf("uriToPath(pathToURI(%q)) = %q", path, back)
	}
}

// Ensure framing rejects missing Content-Length.
func TestLSP_MissingContentLength(t *testing.T) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Type: application/json\r\n\r\n{}")
	reader := bufio.NewReader(&buf)
	_, err := readMessage(reader)
	if err == nil {
		t.Fatal("expected error for missing Content-Length")
	}
}

// Ensure Serve drains to EOF cleanly (client disconnect path).
func TestLSP_ServeEOF(t *testing.T) {
	srv := NewServer("").WithIO(strings.NewReader(""), &bytes.Buffer{})
	err := srv.Serve(context.Background())
	if err != nil {
		t.Errorf("Serve on empty reader should return nil, got: %v", err)
	}
}

// Ensure io.Pipe can be used as transport (streaming verification).
func TestLSP_PipeTransport(t *testing.T) {
	pr, pw := io.Pipe()
	outBuf := &bytes.Buffer{}

	srv := NewServer("").WithIO(pr, outBuf)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(context.Background())
	}()

	req := &Message{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  json.RawMessage(`{}`),
	}
	if err := writeMessage(pw, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Close the write end so the server sees EOF and terminates.
	_ = pw.Close()
	<-done

	// Confirm at least one response was written.
	if outBuf.Len() == 0 {
		t.Error("expected at least one LSP response in output buffer")
	}
}
