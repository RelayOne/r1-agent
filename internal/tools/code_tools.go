// Codebase-graph navigation tools for the native agent loop.
//
// These expose the same graph queries the MCP codebase server serves
// (internal/mcp/codebase_server.go) — symbol search, call graphs, symbol
// usages, impact analysis, file dependencies — as first-class native tools,
// so agents get structured code navigation without an MCP subprocess. Both
// surfaces share internal/codegraph, so results render identically. Tool
// names intentionally match the MCP server's: a config that ALSO registers
// the stoke-codebase MCP server yields distinct mcp_-prefixed names, so
// dispatch stays unambiguous.
//
// Every tool here is read-only over the worktree; none belongs to the
// write-capable set that read-only phases filter out.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/codegraph"
	"github.com/RelayOne/r1/internal/provider"
)

// codeToolsDisabledEnv is the kill switch for the codebase-graph tools
// (R1_DISABLE_* convention, mirrors R1_DISABLE_THROTTLE). When set to "1"
// the tools are neither advertised by Definitions() nor served by Handle()
// — useful for token-sensitive runs, since the seven schemas cost ~1-2KB
// of prompt per request.
const codeToolsDisabledEnv = "R1_DISABLE_CODE_TOOLS"

func codeToolsDisabled() bool {
	return strings.TrimSpace(os.Getenv(codeToolsDisabledEnv)) == "1"
}

// msgCodeToolsDisabled is returned (as a result, not an error) when a
// disabled graph tool is dispatched anyway — e.g. a replayed transcript or
// a model that remembers the tool from an earlier turn.
const msgCodeToolsDisabled = "code navigation tools are disabled (" + codeToolsDisabledEnv + "=1) — use grep/glob instead"

// firstCallNote is appended to every graph tool description: the backing
// index is built lazily on first use and walks the whole worktree, so the
// model should not interpret a slow first call as a hang and retry.
const firstCallNote = " The first graph-tool call builds the code index and may take a few seconds on large repos."

// SetWriteObserver registers a callback invoked with the resolved absolute
// path after every successful write_file/edit_file. Lets embedders refresh
// derived views (e.g. a shared repomap) as the agent mutates the tree.
// Register before dispatch begins; unset means no observation.
func (r *Registry) SetWriteObserver(fn func(absPath string)) {
	r.onFileWrite = fn
}

// noteFileWrite records a successful file write: the lazy code index (if
// already built) is marked dirty so its next query re-reads the tree, and
// the optional write observer fires. Called only after os.WriteFile
// succeeds — failed writes must not invalidate anything.
func (r *Registry) noteFileWrite(absPath string) {
	r.codeMu.Lock()
	if r.code != nil {
		if rel, err := filepath.Rel(r.workDir, absPath); err == nil {
			r.code.MarkDirty(rel)
		}
	}
	r.codeMu.Unlock()
	if r.onFileWrite != nil {
		r.onFileWrite(absPath)
	}
}

// shellIndexInvalidationDisabledEnv is the kill switch for coarse code-index
// invalidation after shell commands (R1_DISABLE_* convention). Shell commands
// mutate the tree in ways the write_file/edit_file hooks never observe — git
// checkout/pull/stash, `sed -i`, code generators, `go run` scaffolding — so
// without invalidation the graph tools serve pre-command results. Set to "1"
// to skip it on runs that never interleave shell mutation with graph queries
// and want to avoid the full-tree rebuild on the next graph call.
const shellIndexInvalidationDisabledEnv = "R1_DISABLE_SHELL_INDEX_INVALIDATION"

func shellIndexInvalidationDisabled() bool {
	return strings.TrimSpace(os.Getenv(shellIndexInvalidationDisabledEnv)) == "1"
}

// noteShellMutation coarsely invalidates the lazily-built code index after a
// shell command (bash/env_exec) that may have mutated the tree. Unlike
// noteFileWrite there is no path list — a shell command can touch anything —
// so it marks the whole tree dirty; refreshLocked rebuilds from disk on the
// next graph query regardless of which paths actually changed. Only touches an
// already-built index (an unbuilt one walks the current tree on first query
// anyway). Gated by the kill switch. Does NOT fire the repomap write observer:
// the observer is keyed on a concrete written path, which a shell command does
// not provide.
func (r *Registry) noteShellMutation(command string) {
	if shellIndexInvalidationDisabled() {
		return
	}
	// Don't force a full four-index rebuild for a command that provably
	// cannot have written source: the agent interleaves graph queries with
	// very frequent read-only shell (grep/ls/go test/git status), and a
	// blanket invalidation made every such query re-walk the whole tree.
	// Conservative: only skip for a single simple invocation of a known
	// read-only program; anything chained, redirected, or unrecognized
	// still invalidates.
	if commandIsReadOnly(command) {
		return
	}
	r.codeMu.Lock()
	if r.code != nil {
		r.code.MarkDirty(".")
	}
	r.codeMu.Unlock()
}

// readOnlyShellPrograms are programs whose normal invocations do not write
// source files. Kept deliberately small and certain; the `go` subcommands
// that DO write (generate) are handled below.
var readOnlyShellPrograms = map[string]bool{
	"ls": true, "cat": true, "grep": true, "egrep": true, "fgrep": true,
	"rg": true, "find": true, "wc": true, "head": true, "tail": true,
	"pwd": true, "echo": true, "which": true, "type": true, "stat": true,
	"file": true, "test": true, "true": true, "false": true, "env": true,
	"basename": true, "dirname": true, "realpath": true, "readlink": true,
	"tree": true, "diff": true, "cmp": true, "sha256sum": true, "md5sum": true,
}

// commandIsReadOnly reports whether cmd is a single simple invocation of a
// program that cannot have written source. It returns false the moment the
// command contains any shell control/redirect operator that could chain a
// writer (&&, ||, ;, |, >, <, `, $(), &), so a "read-only-looking" prefix
// followed by a write is never mistaken for read-only.
func commandIsReadOnly(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return true // nothing ran
	}
	if strings.ContainsAny(cmd, "&|;<>`") || strings.Contains(cmd, "$(") {
		return false
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return true
	}
	prog := fields[0]
	// Strip an env-style leading VAR=val (common: `GOFLAGS=... go test`).
	for len(fields) > 1 && strings.Contains(prog, "=") && !strings.HasPrefix(prog, "=") {
		fields = fields[1:]
		prog = fields[0]
	}
	prog = prog[strings.LastIndex(prog, "/")+1:] // basename
	if prog == "git" && len(fields) > 1 {
		switch fields[1] {
		case "status", "diff", "log", "show", "ls-files", "rev-parse", "branch", "remote", "blame", "grep":
			return true
		}
		return false
	}
	if prog == "go" && len(fields) > 1 {
		switch fields[1] {
		case "build", "test", "vet", "list", "version", "env", "doc", "fmt":
			// build/test/vet/list write only caches/binaries, not source.
			// (`go generate` deliberately absent — it writes source.)
			return true
		}
		return false
	}
	return readOnlyShellPrograms[prog]
}

// codeIndex lazily builds the codebase-graph index over the registry's
// working directory. The build result (or error) is cached for the life of
// the registry — one registry serves one dispatch, so a broken tree fails
// fast instead of re-walking on every query.
func (r *Registry) codeIndex() (*codegraph.Index, error) {
	r.codeMu.Lock()
	defer r.codeMu.Unlock()
	if !r.codeBuilt {
		r.code, r.codeErr = codegraph.Build(r.workDir)
		r.codeBuilt = true
	}
	return r.code, r.codeErr
}

// runCodeTool wraps a graph query with the shared gates: the kill switch
// and index availability. An index build failure is informational, not an
// error — the agent falls back to grep/glob instead of aborting the turn.
func (r *Registry) runCodeTool(query func(ix *codegraph.Index) (string, error)) (string, error) {
	if codeToolsDisabled() {
		return msgCodeToolsDisabled, nil
	}
	ix, err := r.codeIndex()
	if err != nil {
		return fmt.Sprintf("code index unavailable: %v — fall back to grep/glob", err), nil
	}
	return query(ix)
}

// codeToolDefs returns the tool definitions for the codebase-graph tools.
// Schemas mirror the MCP codebase server's so models see one contract for
// the same capability on either surface.
func codeToolDefs() []provider.ToolDef {
	return []provider.ToolDef{
		{
			Name:        "search_symbols",
			Description: "Search for code symbols (functions, types, classes, interfaces) by name prefix. Returns symbol name, kind, file, and line number. Prefer this over grep when looking for a definition." + firstCallNote,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]string{"type": "string", "description": "Symbol name or prefix to search for"},
					"kind":  map[string]string{"type": "string", "description": "Filter by kind: function, method, type, interface, class, variable, constant (empty = all kinds)"},
					"limit": map[string]interface{}{"type": "integer", "description": "Maximum results (default 20)"},
				},
				"required": []string{"query"},
			}),
		},
		{
			Name:        "get_call_graph",
			Description: "Get the call graph for a symbol — who calls it (callers) and what it calls (callees). Uses real AST-parsed call edges for Go files. Essential for understanding code flow and the impact of changing a function." + firstCallNote,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"symbol":    map[string]string{"type": "string", "description": "Symbol name to get the call graph for"},
					"direction": map[string]string{"type": "string", "description": "callers, callees, or both (default: both)"},
					"limit":     map[string]interface{}{"type": "integer", "description": "Maximum results per direction (default 20)"},
				},
				"required": []string{"symbol"},
			}),
		},
		{
			Name:        "find_symbol_usages",
			Description: "Find all files that reference a symbol (function, type, class). Shows where a symbol is consumed across the codebase, with context about what each consuming file defines. Essential for tracing producer/consumer relationships before editing a shared API." + firstCallNote,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"symbol": map[string]string{"type": "string", "description": "Exact symbol name to find usages of"},
					"limit":  map[string]interface{}{"type": "integer", "description": "Maximum results (default 20)"},
				},
				"required": []string{"symbol"},
			}),
		},
		{
			Name:        "impact_analysis",
			Description: "Compute the transitive set of files affected by changes to a given file. Follows the dependency graph to find all direct and indirect dependents. Run this before modifying a widely-imported file." + firstCallNote,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file":  map[string]string{"type": "string", "description": "File path relative to the working directory"},
					"limit": map[string]interface{}{"type": "integer", "description": "Maximum files listed before eliding the rest (default 50)"},
				},
				"required": []string{"file"},
			}),
		},
		{
			Name:        "get_dependencies",
			Description: "Get the import dependencies and reverse dependencies (dependents) of a file. Shows what a file imports and what imports it." + firstCallNote,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file":  map[string]string{"type": "string", "description": "File path relative to the working directory"},
					"limit": map[string]interface{}{"type": "integer", "description": "Maximum entries per list (imports, imported-by) before eliding the rest (default 50)"},
				},
				"required": []string{"file"},
			}),
		},
		{
			Name:        "get_file_symbols",
			Description: "List all symbols (functions, types, classes, methods) defined in a specific file, with line numbers. Cheaper than read_file when you only need a file's structure." + firstCallNote,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file":  map[string]string{"type": "string", "description": "File path relative to the working directory"},
					"limit": map[string]interface{}{"type": "integer", "description": "Maximum symbols listed before eliding the rest (default 50)"},
				},
				"required": []string{"file"},
			}),
		},
		{
			Name:        "search_content",
			Description: "Semantic content search across the codebase: finds files whose content is most relevant to a natural-language query using TF-IDF ranking. Use for conceptual queries where grep's exact matching is too brittle." + firstCallNote,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]string{"type": "string", "description": "Natural language query describing what you're looking for"},
					"limit": map[string]interface{}{"type": "integer", "description": "Maximum results (default 10)"},
				},
				"required": []string{"query"},
			}),
		},
	}
}

// --- Handlers ---

func (r *Registry) handleSearchSymbols(input json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Kind  string `json:"kind"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	return r.runCodeTool(func(ix *codegraph.Index) (string, error) {
		return ix.SearchSymbols(args.Query, args.Kind, args.Limit)
	})
}

func (r *Registry) handleGetCallGraph(input json.RawMessage) (string, error) {
	var args struct {
		Symbol    string `json:"symbol"`
		Direction string `json:"direction"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	return r.runCodeTool(func(ix *codegraph.Index) (string, error) {
		return ix.GetCallGraph(args.Symbol, args.Direction, args.Limit)
	})
}

func (r *Registry) handleFindSymbolUsages(input json.RawMessage) (string, error) {
	var args struct {
		Symbol string `json:"symbol"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	return r.runCodeTool(func(ix *codegraph.Index) (string, error) {
		return ix.FindSymbolUsages(args.Symbol, args.Limit)
	})
}

func (r *Registry) handleImpactAnalysis(input json.RawMessage) (string, error) {
	var args struct {
		File  string `json:"file"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	return r.runCodeTool(func(ix *codegraph.Index) (string, error) {
		return ix.ImpactAnalysis(args.File, args.Limit)
	})
}

func (r *Registry) handleGetDependencies(input json.RawMessage) (string, error) {
	var args struct {
		File  string `json:"file"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	return r.runCodeTool(func(ix *codegraph.Index) (string, error) {
		return ix.GetDependencies(args.File, args.Limit)
	})
}

func (r *Registry) handleGetFileSymbols(input json.RawMessage) (string, error) {
	var args struct {
		File  string `json:"file"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	return r.runCodeTool(func(ix *codegraph.Index) (string, error) {
		return ix.GetFileSymbols(args.File, args.Limit)
	})
}

func (r *Registry) handleSearchContent(input json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	return r.runCodeTool(func(ix *codegraph.Index) (string, error) {
		return ix.SearchContent(args.Query, args.Limit)
	})
}
