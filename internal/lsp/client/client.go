// Package client implements a generic Language Server Protocol (LSP) 3.17
// JSON-RPC 2.0 client over stdio. It is the counterpart to the LSP server
// in the parent package: where `internal/lsp` exposes R1's symbol index AS
// an LSP server, this sub-package speaks LSP TO external language servers
// (gopls, pyright, typescript-language-server, rust-analyzer) so that the
// agent can request diagnostics, completions, and hovers at edit-time.
//
// T-R1P-020: Multi-language LSP integration.
//
// Public surface:
//
//	c, err := client.LaunchGo(rootDir)            // or LaunchPython, LaunchTypeScript, LaunchRust
//	if err := c.Initialize(rootURI); err != nil { ... }
//	c.OpenDocument(uri, "go", text)
//	items, _ := c.Completion(uri, line, char)
//	hover, _ := c.Hover(uri, line, char)
//	diags, _ := c.Diagnostics(uri)
//	c.Shutdown()
//
// Wire format (per LSP 3.17): Content-Length-framed JSON-RPC 2.0 over a
// duplex byte stream. The default transport is os/exec stdin+stdout, but
// any io.ReadWriteCloser pair works (used by the unit tests with net.Pipe).
//
// Diagnostics are server-pushed via the `textDocument/publishDiagnostics`
// notification. The client buffers the most recent diagnostics per URI so
// callers can poll synchronously after any text-modifying request.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client is a stateful LSP 3.17 client. One Client wraps one running
// language-server process (or any other transport) and one workspace.
//
// All exported methods are safe to call from multiple goroutines.
type Client struct {
	// transport plumbing
	cmd    *exec.Cmd          // nil when constructed via WithTransport
	stdin  io.WriteCloser     // request sink
	stdout io.ReadCloser      // response/notification source
	closer func() error       // cleanup hook (kill process, close pipes)

	// JSON-RPC bookkeeping
	nextID  atomic.Int64       // monotonically increasing request IDs
	pending sync.Map           // map[int64]chan *response — outstanding requests

	// server state
	diagMu      sync.RWMutex
	diagnostics map[string][]Diagnostic // last known diagnostics per URI

	// lifecycle
	readerDone chan struct{}   // closed when the read loop exits
	readerErr  atomic.Value    // error, if reader exited unexpectedly

	// configuration
	requestTimeout time.Duration
}

// Diagnostic mirrors the LSP `Diagnostic` payload (subset).
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"` // 1=Error, 2=Warning, 3=Info, 4=Hint
	Code     string `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// Range is an LSP source range (zero-indexed lines/characters).
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Position is a zero-indexed line+character offset.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// CompletionItem is a trimmed LSP CompletionItem.
type CompletionItem struct {
	Label         string `json:"label"`
	Kind          int    `json:"kind,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Documentation string `json:"documentation,omitempty"`
	InsertText    string `json:"insertText,omitempty"`
}

// HoverResult is the textual hover content returned by the server.
type HoverResult struct {
	Contents string `json:"contents"`
}

// --- launchers ---

// LaunchGo starts `gopls` for the given workspace root and returns a Client.
// Returns an error if `gopls` is not on PATH.
func LaunchGo(root string) (*Client, error) {
	return launchProcess(root, "gopls", []string{"serve"})
}

// LaunchPython starts `pyright-langserver` (preferred) or `pylsp` for root.
// Returns an error if neither binary is on PATH.
func LaunchPython(root string) (*Client, error) {
	if _, err := exec.LookPath("pyright-langserver"); err == nil {
		return launchProcess(root, "pyright-langserver", []string{"--stdio"})
	}
	if _, err := exec.LookPath("pylsp"); err == nil {
		return launchProcess(root, "pylsp", nil)
	}
	return nil, errors.New("lsp/client: neither pyright-langserver nor pylsp found on PATH")
}

// LaunchTypeScript starts `typescript-language-server --stdio` for root.
func LaunchTypeScript(root string) (*Client, error) {
	return launchProcess(root, "typescript-language-server", []string{"--stdio"})
}

// LaunchRust starts `rust-analyzer` for root.
func LaunchRust(root string) (*Client, error) {
	return launchProcess(root, "rust-analyzer", nil)
}

// LaunchByLanguage dispatches to the right launcher for the given language
// id (one of "go", "python", "typescript", "javascript", "rust"). Returns
// an error for unknown ids or missing binaries.
func LaunchByLanguage(lang, root string) (*Client, error) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "go":
		return LaunchGo(root)
	case "python", "py":
		return LaunchPython(root)
	case "typescript", "javascript", "ts", "js":
		return LaunchTypeScript(root)
	case "rust", "rs":
		return LaunchRust(root)
	default:
		return nil, fmt.Errorf("lsp/client: no launcher for language %q", lang)
	}
}

// launchProcess is the shared launcher implementation.
func launchProcess(root, bin string, args []string) (*Client, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("lsp/client: %s not found on PATH: %w", bin, err)
	}
	cmd := exec.Command(bin, args...)
	if root != "" {
		cmd.Dir = root
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp/client: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("lsp/client: stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("lsp/client: start: %w", err)
	}
	c := newClient(stdin, stdout, func() error {
		_ = stdin.Close()
		_ = stdout.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return nil
	})
	c.cmd = cmd
	return c, nil
}

// WithTransport constructs a Client from an arbitrary stdin/stdout pair.
// Used by tests with `net.Pipe()` or `io.Pipe()` to swap in a fake server
// without a real subprocess.
func WithTransport(stdin io.WriteCloser, stdout io.ReadCloser) *Client {
	return newClient(stdin, stdout, func() error {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil
	})
}

func newClient(stdin io.WriteCloser, stdout io.ReadCloser, closer func() error) *Client {
	c := &Client{
		stdin:          stdin,
		stdout:         stdout,
		closer:         closer,
		diagnostics:    make(map[string][]Diagnostic),
		readerDone:     make(chan struct{}),
		requestTimeout: 30 * time.Second,
	}
	go c.readLoop()
	return c
}

// SetRequestTimeout adjusts how long Initialize/Completion/Hover wait for a
// reply before returning a timeout error. Default 30s.
func (c *Client) SetRequestTimeout(d time.Duration) {
	if d > 0 {
		c.requestTimeout = d
	}
}

// --- public LSP operations ---

// Initialize sends the LSP `initialize` request with the given root URI and
// then sends the `initialized` notification. Returns an error if the server
// rejects the handshake or the connection drops.
func (c *Client) Initialize(rootURI string) error {
	params := map[string]interface{}{
		"processId":    nil,
		"rootUri":      rootURI,
		"capabilities": map[string]interface{}{
			"textDocument": map[string]interface{}{
				"publishDiagnostics": map[string]interface{}{
					"relatedInformation": true,
				},
				"hover":      map[string]interface{}{},
				"completion": map[string]interface{}{},
			},
		},
	}
	if _, err := c.request("initialize", params); err != nil {
		return fmt.Errorf("lsp/client: initialize: %w", err)
	}
	if err := c.notify("initialized", map[string]interface{}{}); err != nil {
		return fmt.Errorf("lsp/client: initialized: %w", err)
	}
	return nil
}

// OpenDocument sends `textDocument/didOpen` so the server starts tracking
// the buffer. Lang is the LSP languageId ("go", "python", etc.).
func (c *Client) OpenDocument(uri, lang, text string) error {
	return c.notify("textDocument/didOpen", map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri":        uri,
			"languageId": lang,
			"version":    1,
			"text":       text,
		},
	})
}

// Completion requests completions at the cursor and returns the items.
func (c *Client) Completion(uri string, line, char int) ([]CompletionItem, error) {
	raw, err := c.request("textDocument/completion", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
		"position":     map[string]interface{}{"line": line, "character": char},
	})
	if err != nil {
		return nil, err
	}
	// Server may return a CompletionList or a bare []CompletionItem.
	var list struct {
		Items []CompletionItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err == nil && list.Items != nil {
		return list.Items, nil
	}
	var items []CompletionItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("lsp/client: completion result: %w", err)
	}
	return items, nil
}

// Hover requests hover documentation at the cursor.
func (c *Client) Hover(uri string, line, char int) (*HoverResult, error) {
	raw, err := c.request("textDocument/hover", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
		"position":     map[string]interface{}{"line": line, "character": char},
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// Hover.contents may be a string, a {kind,value} object, or an array.
	var probe struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("lsp/client: hover result: %w", err)
	}
	return &HoverResult{Contents: flattenHoverContents(probe.Contents)}, nil
}

// Diagnostics returns the most recent diagnostics buffered for uri.
// Diagnostics are pushed by the server asynchronously via
// `textDocument/publishDiagnostics`; this method returns whatever has
// arrived so far (possibly empty).
func (c *Client) Diagnostics(uri string) ([]Diagnostic, error) {
	if err := c.readerErrIfAny(); err != nil {
		return nil, err
	}
	c.diagMu.RLock()
	defer c.diagMu.RUnlock()
	out := make([]Diagnostic, len(c.diagnostics[uri]))
	copy(out, c.diagnostics[uri])
	return out, nil
}

// Shutdown sends the LSP `shutdown` request followed by the `exit`
// notification, then closes the transport. Safe to call multiple times.
func (c *Client) Shutdown() error {
	// Best-effort: server may already be dead; ignore errors and still close.
	_, _, _ = c.requestNoWait("shutdown", nil)
	_ = c.notify("exit", nil)
	return c.closer()
}

// --- helpers ---

// PathToURI converts a filesystem path to a `file://` URI. Useful for
// callers building textDocument identifiers.
func PathToURI(path string) string {
	if strings.HasPrefix(path, "file://") {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file://" + abs
}

// URIToPath strips the `file://` scheme from a URI.
func URIToPath(uri string) string {
	if strings.HasPrefix(uri, "file://") {
		return uri[len("file://"):]
	}
	return uri
}

func flattenHoverContents(raw json.RawMessage) string {
	trim := strings.TrimSpace(string(raw))
	if trim == "" || trim == "null" {
		return ""
	}
	// String form.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// {kind,value} form.
	var mc struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &mc); err == nil && mc.Value != "" {
		return mc.Value
	}
	// []MarkedString form.
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		var sb strings.Builder
		for i, it := range arr {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(flattenHoverContents(it))
		}
		return sb.String()
	}
	return trim
}

// --- JSON-RPC framing & dispatch ---

// rpcRequest is an outbound JSON-RPC 2.0 request.
type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// rpcNotification is an outbound JSON-RPC 2.0 notification (no ID).
type rpcNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// rpcResponse is an inbound JSON-RPC 2.0 response or notification.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcErrorBody   `json:"error,omitempty"`
}

type rpcErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	result json.RawMessage
	err    error
}

// request sends a synchronous request and waits up to requestTimeout.
func (c *Client) request(method string, params interface{}) (json.RawMessage, error) {
	ch, id, err := c.requestNoWait(method, params)
	if err != nil {
		return nil, err
	}
	defer c.pending.Delete(id)

	timer := time.NewTimer(c.requestTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.result, r.err
	case <-timer.C:
		return nil, fmt.Errorf("lsp/client: %s timed out after %s", method, c.requestTimeout)
	case <-c.readerDone:
		if err := c.readerErrIfAny(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("lsp/client: connection closed during %s", method)
	}
}

// requestNoWait sends a request and returns the reply channel without blocking.
func (c *Client) requestNoWait(method string, params interface{}) (chan *response, int64, error) {
	id := c.nextID.Add(1)
	ch := make(chan *response, 1)
	c.pending.Store(id, ch)

	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if err := c.writeFrame(req); err != nil {
		c.pending.Delete(id)
		return nil, id, err
	}
	return ch, id, nil
}

// notify sends a fire-and-forget notification (no response expected).
func (c *Client) notify(method string, params interface{}) error {
	return c.writeFrame(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

// writeFrame serializes v and writes one Content-Length-framed JSON message.
func (c *Client) writeFrame(v interface{}) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("lsp/client: marshal: %w", err)
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return fmt.Errorf("lsp/client: write header: %w", err)
	}
	if _, err := c.stdin.Write(body); err != nil {
		return fmt.Errorf("lsp/client: write body: %w", err)
	}
	return nil
}

// readLoop consumes framed messages from stdout and dispatches them to
// pending request channels (responses) or the diagnostics buffer
// (notifications). Exits when the stream closes or returns an error.
func (c *Client) readLoop() {
	defer close(c.readerDone)
	defer c.failPending()

	r := bufio.NewReader(c.stdout)
	for {
		body, err := readFrame(r)
		if err != nil {
			if err != io.EOF {
				c.readerErr.Store(err)
			}
			return
		}
		var msg rpcResponse
		if err := json.Unmarshal(body, &msg); err != nil {
			c.readerErr.Store(fmt.Errorf("lsp/client: decode frame: %w", err))
			return
		}
		c.dispatch(&msg)
	}
}

// failPending wakes up every outstanding request with a connection-closed
// error so callers don't deadlock when the server dies.
func (c *Client) failPending() {
	c.pending.Range(func(k, v interface{}) bool {
		ch, ok := v.(chan *response)
		if !ok {
			return true
		}
		select {
		case ch <- &response{err: errors.New("lsp/client: connection closed")}:
		default:
		}
		return true
	})
}

// dispatch routes one parsed message either to its waiting request channel
// or — for server-initiated notifications — to the diagnostics buffer.
func (c *Client) dispatch(msg *rpcResponse) {
	if msg.ID != nil {
		ch, ok := c.pending.Load(*msg.ID)
		if !ok {
			return // unknown response (already timed out)
		}
		out := ch.(chan *response)
		if msg.Error != nil {
			out <- &response{err: fmt.Errorf("lsp/client: rpc error %d: %s", msg.Error.Code, msg.Error.Message)}
			return
		}
		out <- &response{result: msg.Result}
		return
	}
	// Notification.
	switch msg.Method {
	case "textDocument/publishDiagnostics":
		c.handlePublishDiagnostics(msg.Params)
	default:
		// Many notifications (window/logMessage, $/progress, etc.) are
		// informational; ignore them silently.
	}
}

func (c *Client) handlePublishDiagnostics(params json.RawMessage) {
	var p struct {
		URI         string       `json:"uri"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	c.diagMu.Lock()
	c.diagnostics[p.URI] = p.Diagnostics
	c.diagMu.Unlock()
}

// readFrame reads one Content-Length-framed message body from r.
func readFrame(r *bufio.Reader) ([]byte, error) {
	contentLen := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			n, perr := strconv.Atoi(val)
			if perr != nil {
				return nil, fmt.Errorf("lsp/client: bad Content-Length %q", val)
			}
			contentLen = n
		}
	}
	if contentLen < 0 {
		return nil, errors.New("lsp/client: missing Content-Length header")
	}
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("lsp/client: read body: %w", err)
	}
	return body, nil
}

func (c *Client) readerErrIfAny() error {
	if v := c.readerErr.Load(); v != nil {
		if e, ok := v.(error); ok && e != nil {
			return e
		}
	}
	return nil
}

// WaitForReader blocks until the read loop exits. Intended for tests and
// graceful shutdown — production callers should use Shutdown() instead.
func (c *Client) WaitForReader(ctx context.Context) error {
	select {
	case <-c.readerDone:
		return c.readerErrIfAny()
	case <-ctx.Done():
		return ctx.Err()
	}
}
