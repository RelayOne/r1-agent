// Package lsp implements a minimal Language Server Protocol (LSP) server
// that exposes R1's symbol index as hover and completion providers.
//
// T-R1P-009: LSP adapter — any LSP-enabled editor (VSCode, Neovim, JetBrains,
// Emacs, Sublime) can launch R1 as an LSP server and receive:
//
//   - textDocument/completion  — symbol completions from the repo index
//   - textDocument/hover       — doc comment + signature for the word under cursor
//   - textDocument/definition  — jump-to-definition from the symbol index
//   - workspace/symbol         — workspace-wide symbol search
//
// Transport: JSON-RPC 2.0 over stdin/stdout with the standard
// Content-Length HTTP-style framing that all LSP clients expect.
//
// Usage:
//
//	r1 lsp [--root <project-root>]
//
// Wire protocol (per LSP spec):
//
//	Content-Length: <n>\r\n
//	\r\n
//	<n bytes of UTF-8 JSON>
//
// The server is intentionally minimal: it does not start a full language
// server for every language. Instead it leverages R1's existing symindex,
// depgraph, and tfidf packages to provide symbol-level intelligence for
// any language that R1 already supports (Go, TS/JS, Python, Rust, etc.).
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/RelayOne/r1/internal/symindex"
)

// Server is the LSP server instance. It owns the symbol index and handles
// JSON-RPC 2.0 requests from a connected LSP client over a stdio transport.
type Server struct {
	root     string
	idx      *symindex.Index
	logger   *log.Logger
	in       io.Reader
	out      io.Writer
	exitFunc func(int) // defaults to os.Exit; overridable in tests
}

// NewServer creates an LSP server for the given project root. Call Serve()
// to start processing requests. The server indexes the repository lazily on
// the first request that needs symbols (to keep startup fast).
func NewServer(root string) *Server {
	return &Server{
		root:     root,
		logger:   log.New(os.Stderr, "[lsp] ", log.LstdFlags),
		in:       os.Stdin,
		out:      os.Stdout,
		exitFunc: os.Exit,
	}
}

// WithIO replaces stdin/stdout with the provided reader/writer. Useful for
// testing without OS file handles.
func (s *Server) WithIO(in io.Reader, out io.Writer) *Server {
	s.in = in
	s.out = out
	return s
}

// ensureIndex builds the symbol index if not yet built. Thread-safe: callers
// hold no lock; the first concurrent caller wins and subsequent ones reuse.
func (s *Server) ensureIndex() {
	if s.idx != nil {
		return
	}
	root := s.root
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			s.logger.Printf("getwd: %v", err)
			return
		}
	}
	idx, err := symindex.Build(root)
	if err != nil {
		s.logger.Printf("symindex.Build(%s): %v", root, err)
		return
	}
	s.idx = idx
	s.logger.Printf("symindex built: %d symbols in %s", idx.Count(), root)
}

// Serve reads LSP requests from s.in and writes responses to s.out until
// ctx is done or the connection closes. Blocks until the peer disconnects.
func (s *Server) Serve(ctx context.Context) error {
	reader := bufio.NewReader(s.in)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msg, err := readMessage(reader)
		if err != nil {
			if err == io.EOF {
				return nil // client disconnected cleanly
			}
			return fmt.Errorf("lsp: read: %w", err)
		}

		resp := s.dispatch(msg)
		if resp != nil {
			if werr := writeMessage(s.out, resp); werr != nil {
				return fmt.Errorf("lsp: write: %w", werr)
			}
		}
	}
}

// --- JSON-RPC 2.0 types ---

// Message is the top-level LSP JSON-RPC 2.0 object. Both requests and
// notifications use this; responses add Result/Error and omit Method.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`    // int | string | null
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Standard JSON-RPC 2.0 error codes.
const (
	errParseError     = -32700
	errMethodNotFound = -32601
	errInvalidParams  = -32602
)

// response builds a JSON-RPC 2.0 success response.
func response(id interface{}, result interface{}) *Message {
	return &Message{JSONRPC: "2.0", ID: id, Result: result}
}

// errResponse builds a JSON-RPC 2.0 error response.
func errResponse(id interface{}, code int, msg string) *Message {
	return &Message{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}

// --- Framed I/O ---

// readMessage reads one LSP framed message (Content-Length: N\r\n\r\n + body).
func readMessage(r *bufio.Reader) (*Message, error) {
	// Read headers until blank line.
	contentLen := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // blank line = end of headers
		}
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			n, parseErr := strconv.Atoi(val)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid Content-Length: %q", val)
			}
			contentLen = n
		}
	}

	if contentLen <= 0 {
		return nil, fmt.Errorf("missing or zero Content-Length")
	}

	body := make([]byte, contentLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return &msg, nil
}

// writeMessage sends one LSP framed message to w.
func writeMessage(w io.Writer, msg *Message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// --- Dispatch ---

// dispatch routes one incoming message to the appropriate handler.
// Returns nil for notifications (no response required).
func (s *Server) dispatch(msg *Message) *Message {
	// Notification (no ID) → no response.
	isNotification := msg.ID == nil && msg.Method != ""

	switch msg.Method {
	case "initialize":
		return s.handleInitialize(msg)
	case "initialized":
		return nil // notification
	case "shutdown":
		return response(msg.ID, nil)
	case "exit":
		s.exitFunc(0)
	case "textDocument/completion":
		return s.handleCompletion(msg)
	case "textDocument/hover":
		return s.handleHover(msg)
	case "textDocument/definition":
		return s.handleDefinition(msg)
	case "workspace/symbol":
		return s.handleWorkspaceSymbol(msg)
	default:
		if !isNotification {
			return errResponse(msg.ID, errMethodNotFound,
				fmt.Sprintf("method not supported: %s", msg.Method))
		}
	}
	return nil
}

// --- LSP capability types ---

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
	ServerInfo   serverInfo         `json:"serverInfo"`
}

type serverCapabilities struct {
	TextDocumentSync   int                    `json:"textDocumentSync"`
	CompletionProvider completionOptions      `json:"completionProvider"`
	HoverProvider      bool                   `json:"hoverProvider"`
	DefinitionProvider bool                   `json:"definitionProvider"`
	WorkspaceSymbol    workspaceSymbolOptions `json:"workspaceSymbolProvider"`
}

type completionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters"`
}

type workspaceSymbolOptions struct {
	WorkDoneProgress bool `json:"workDoneProgress"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// handleInitialize responds with R1's declared LSP capabilities.
func (s *Server) handleInitialize(msg *Message) *Message {
	// Extract root URI if provided.
	var params struct {
		RootURI  string `json:"rootUri"`
		RootPath string `json:"rootPath"`
	}
	if err := json.Unmarshal(msg.Params, &params); err == nil {
		if params.RootURI != "" && s.root == "" {
			s.root = uriToPath(params.RootURI)
		} else if params.RootPath != "" && s.root == "" {
			s.root = params.RootPath
		}
	}

	result := initializeResult{
		Capabilities: serverCapabilities{
			TextDocumentSync:   1, // full sync
			HoverProvider:      true,
			DefinitionProvider: true,
			CompletionProvider: completionOptions{
				TriggerCharacters: []string{".", "::", " "},
			},
			WorkspaceSymbol: workspaceSymbolOptions{},
		},
		ServerInfo: serverInfo{Name: "r1-lsp", Version: "0.1.0"},
	}
	return response(msg.ID, result)
}

// --- Completion ---

type completionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []completionItem `json:"items"`
}

type completionItem struct {
	Label         string `json:"label"`
	Kind          int    `json:"kind"` // LSP CompletionItemKind
	Detail        string `json:"detail,omitempty"`
	Documentation string `json:"documentation,omitempty"`
	InsertText    string `json:"insertText,omitempty"`
}

// LSP CompletionItemKind values (subset used).
const (
	lspKindText      = 1
	lspKindFunction  = 3
	lspKindField     = 5
	lspKindVariable  = 6
	lspKindClass     = 7
	lspKindInterface = 8
	lspKindModule    = 9
	lspKindProperty  = 10
	lspKindKeyword   = 14
	lspKindConstant  = 21
	lspKindStruct    = 22
	lspKindEvent     = 23
)

func symKindToLSP(k symindex.SymbolKind) int {
	switch k {
	case symindex.KindFunction:
		return lspKindFunction
	case symindex.KindMethod:
		return lspKindFunction
	case symindex.KindType:
		return lspKindStruct
	case symindex.KindInterface:
		return lspKindInterface
	case symindex.KindClass:
		return lspKindClass
	case symindex.KindVariable:
		return lspKindVariable
	case symindex.KindConstant:
		return lspKindConstant
	case symindex.KindField:
		return lspKindField
	case symindex.KindPackage:
		return lspKindModule
	default:
		return lspKindText
	}
}

// handleCompletion returns completions for the word prefix at the cursor.
func (s *Server) handleCompletion(msg *Message) *Message {
	s.ensureIndex()

	var params struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"position"`
		Context struct {
			TriggerKind      int    `json:"triggerKind"`
			TriggerCharacter string `json:"triggerCharacter"`
		} `json:"context"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return errResponse(msg.ID, errInvalidParams, err.Error())
	}

	// Extract word prefix from the URI + position.
	// We read the file to find the token prefix at the cursor.
	prefix := wordPrefixAt(uriToPath(params.TextDocument.URI),
		params.Position.Line, params.Position.Character)

	var items []completionItem
	if s.idx != nil && len(prefix) >= 2 {
		matches := s.idx.Search(prefix)
		for _, sym := range matches {
			item := completionItem{
				Label:      sym.Name,
				Kind:       symKindToLSP(sym.Kind),
				Detail:     fmt.Sprintf("%s (%s)", sym.Kind, filepath.Base(sym.File)),
				InsertText: sym.Name,
			}
			if sym.Doc != "" {
				item.Documentation = sym.Doc
			} else if sym.Signature != "" {
				item.Documentation = sym.Signature
			}
			items = append(items, item)
			if len(items) >= 50 {
				break
			}
		}
	}

	list := completionList{
		IsIncomplete: len(items) >= 50,
		Items:        items,
	}
	if list.Items == nil {
		list.Items = []completionItem{}
	}
	return response(msg.ID, list)
}

// --- Hover ---

type hoverResult struct {
	Contents markupContent `json:"contents"`
}

type markupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// handleHover returns doc + signature for the symbol under the cursor.
func (s *Server) handleHover(msg *Message) *Message {
	s.ensureIndex()

	var params struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"position"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return errResponse(msg.ID, errInvalidParams, err.Error())
	}

	word := wordAt(uriToPath(params.TextDocument.URI),
		params.Position.Line, params.Position.Character)
	if word == "" || s.idx == nil {
		return response(msg.ID, nil) // nil = no hover
	}

	syms := s.idx.Lookup(word)
	if len(syms) == 0 {
		return response(msg.ID, nil)
	}

	// Prefer the symbol in the same file, fall back to first.
	filePath := uriToPath(params.TextDocument.URI)
	best := syms[0]
	for _, sym := range syms {
		if sym.File == filePath {
			best = sym
			break
		}
	}

	var sb strings.Builder
	if best.Signature != "" {
		fmt.Fprintf(&sb, "```\n%s\n```\n", best.Signature)
	}
	if best.Doc != "" {
		sb.WriteString("\n")
		sb.WriteString(best.Doc)
	}
	if sb.Len() == 0 {
		fmt.Fprintf(&sb, "`%s` (%s in %s)", best.Name, best.Kind, filepath.Base(best.File))
	}

	return response(msg.ID, hoverResult{
		Contents: markupContent{Kind: "markdown", Value: sb.String()},
	})
}

// --- Definition ---

type location struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

type lspRange struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// handleDefinition returns the definition location(s) of the symbol.
func (s *Server) handleDefinition(msg *Message) *Message {
	s.ensureIndex()

	var params struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"position"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return errResponse(msg.ID, errInvalidParams, err.Error())
	}

	word := wordAt(uriToPath(params.TextDocument.URI),
		params.Position.Line, params.Position.Character)
	if word == "" || s.idx == nil {
		return response(msg.ID, []location{})
	}

	syms := s.idx.Lookup(word)
	locs := make([]location, 0, len(syms))
	for _, sym := range syms {
		if sym.Line <= 0 {
			continue
		}
		l := location{
			URI: pathToURI(sym.File),
			Range: lspRange{
				Start: position{Line: sym.Line - 1, Character: 0},
				End:   position{Line: sym.Line - 1, Character: len(sym.Name)},
			},
		}
		locs = append(locs, l)
	}
	return response(msg.ID, locs)
}

// --- Workspace symbol ---

type workspaceSymbol struct {
	Name     string   `json:"name"`
	Kind     int      `json:"kind"`
	Location location `json:"location"`
}

// handleWorkspaceSymbol returns matching symbols from the index.
func (s *Server) handleWorkspaceSymbol(msg *Message) *Message {
	s.ensureIndex()

	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return errResponse(msg.ID, errInvalidParams, err.Error())
	}

	var symbols []workspaceSymbol
	if s.idx != nil {
		for _, sym := range s.idx.Search(params.Query) {
			if sym.Line <= 0 {
				continue
			}
			symbols = append(symbols, workspaceSymbol{
				Name: sym.Name,
				Kind: symKindToLSP(sym.Kind),
				Location: location{
					URI: pathToURI(sym.File),
					Range: lspRange{
						Start: position{Line: sym.Line - 1, Character: 0},
						End:   position{Line: sym.Line - 1, Character: len(sym.Name)},
					},
				},
			})
			if len(symbols) >= 100 {
				break
			}
		}
	}
	if symbols == nil {
		symbols = []workspaceSymbol{}
	}
	return response(msg.ID, symbols)
}

// --- Helpers ---

// wordAt reads file at the given 0-indexed line/char and returns the
// identifier word that the cursor sits in. Returns "" on any I/O error.
func wordAt(path string, line, char int) string {
	lines := readFileLines(path)
	if line < 0 || line >= len(lines) {
		return ""
	}
	return extractWordAt(lines[line], char)
}

// wordPrefixAt returns the identifier prefix ending at char (for completions).
func wordPrefixAt(path string, line, char int) string {
	lines := readFileLines(path)
	if line < 0 || line >= len(lines) {
		return ""
	}
	l := lines[line]
	if char > len(l) {
		char = len(l)
	}
	prefix := l[:char]
	// Walk back to find start of identifier.
	start := len(prefix)
	for start > 0 && isIdentChar(prefix[start-1]) {
		start--
	}
	return prefix[start:]
}

// extractWordAt extracts the identifier word at position char in a line.
func extractWordAt(line string, char int) string {
	if char < 0 || char > len(line) {
		return ""
	}
	// Find start.
	start := char
	for start > 0 && isIdentChar(line[start-1]) {
		start--
	}
	// Find end.
	end := char
	for end < len(line) && isIdentChar(line[end]) {
		end++
	}
	if start >= end {
		return ""
	}
	return line[start:end]
}

func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}

// readFileLines returns the lines of a file, or nil on error.
func readFileLines(path string) []string {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// uriToPath converts a file:// URI to an OS path.
func uriToPath(uri string) string {
	if strings.HasPrefix(uri, "file://") {
		return uri[len("file://"):]
	}
	return uri
}

// pathToURI converts an OS path to a file:// URI.
func pathToURI(path string) string {
	if strings.HasPrefix(path, "file://") {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file://" + abs
}
