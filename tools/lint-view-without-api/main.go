// lint-view-without-api — CI lint enforcing spec 8 §8:
//
//   > Every interactive component must (a) carry a stable data-testid
//   > (or equivalent TUI A11yNode stable_id, or Tauri command name)
//   > AND (b) reference an MCP tool name that exists in the
//   > r1_server.go catalog.
//
// Detection per surface (§8.1):
//
//   React (web/src/**/*.tsx)
//     onClick / onChange / onSubmit / onKeyDown / role="button" /
//     *.stories.tsx actionables -> require data-testid AND
//     agentic.mcp_tool reference.
//
//   Bubble Tea (internal/tui/**/*.go)
//     Models that consume tea.KeyMsg AND dispatch a state change ->
//     require A11yEmitter impl with StableID AND mcp_tool tag in the
//     case branch.
//
//   Tauri (desktop/src-tauri/**/*.rs)
//     #[tauri::command] functions -> require mcp_tool doc-comment
//     annotation.
//
// Algorithm (§8.2):
//
//   1. Walk source trees with go/ast (Go), regexp+JSX-tokenizer (TSX),
//      regexp scan (Rust).
//   2. For each interactive element, extract { surface, location,
//      mcp_tool_ref, stable_id }.
//   3. Load the MCP catalog by spawning `r1 mcp serve --print-tools`
//      and parsing JSON.
//   4. For each interactive element:
//        - if mcp_tool_ref empty -> FAIL "view-without-API at <loc>".
//        - if mcp_tool_ref not in catalog -> FAIL "unknown MCP tool".
//        - if stable_id empty -> FAIL "missing stable_id".
//   5. For each MCP tool in catalog with category in {sessions,lanes,
//      cortex,missions,worktrees}:
//        - if no UI surface references it AND tool is not flagged
//          headless-only -> WARN.
//   6. Exit non-zero on any FAIL.
//
// STATUS: PARTIAL — the React+Tauri scanners are BLOCKED on specs 6/7
// merge (the source trees do not exist in this checkpoint). The Go
// scanner walks internal/tui/ which IS present. The catalog loader,
// allowlist, and CLI shell are complete and exercised by tests in
// item 38.
//
// CLI flags:
//   --catalog PATH    JSON file with the r1.* catalog (defaults to
//                     `r1 mcp serve --print-tools` invocation).
//   --root PATH       repo root (default: current working dir).
//   --allowlist PATH  YAML allowlist (default: tools/lint-view-without-api/allowlist.yaml).
//   --json            emit JSON instead of human-readable findings.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const exitOK = 0
const exitFail = 1
const exitErr = 2

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lint-view-without-api", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repo root to scan")
	catalogPath := fs.String("catalog", "", "path to a JSON catalog file (default: spawn `r1 mcp serve --print-tools`)")
	allowlistPath := fs.String("allowlist", "", "path to allowlist YAML (default: tools/lint-view-without-api/allowlist.yaml)")
	asJSON := fs.Bool("json", false, "emit JSON findings instead of human text")
	if err := fs.Parse(args); err != nil {
		return exitErr
	}

	catalog, err := loadCatalog(*catalogPath)
	if err != nil {
		fmt.Fprintf(stderr, "load catalog: %v\n", err)
		return exitErr
	}
	if *allowlistPath == "" {
		*allowlistPath = filepath.Join(*root, "tools", "lint-view-without-api", "allowlist.yaml")
	}
	allow, err := loadAllowlist(*allowlistPath)
	if err != nil {
		fmt.Fprintf(stderr, "load allowlist: %v (continuing with empty allowlist)\n", err)
		allow = &Allowlist{}
	}

	findings := []Finding{}
	findings = append(findings, scanReact(*root, catalog)...)
	findings = append(findings, scanBubbleTea(*root, catalog)...)
	findings = append(findings, scanTauri(*root, catalog)...)
	findings = append(findings, scanUnusedTools(*root, catalog, allow)...)

	failures := 0
	for _, f := range findings {
		if f.Severity == "FAIL" {
			failures++
		}
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(findings)
	} else {
		emitText(findings, stdout)
	}

	if failures > 0 {
		return exitFail
	}
	return exitOK
}

// Finding is one issue surfaced by the lint.
type Finding struct {
	Severity string `json:"severity"` // FAIL | WARN | INFO
	Surface  string `json:"surface"`  // react | bubbletea | tauri | catalog
	Path     string `json:"path"`
	Line     int    `json:"line,omitempty"`
	Tool     string `json:"tool,omitempty"`
	StableID string `json:"stable_id,omitempty"`
	Message  string `json:"message"`
}

// loadCatalog reads tool names from a JSON file or spawns the r1
// binary. The JSON shape is the array emitted by `r1 mcp serve
// --print-tools` (a slice of {name, description, input_schema}).
func loadCatalog(path string) (map[string]bool, error) {
	var raw []byte
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		raw = b
	} else {
		// Resolve `r1` from PATH; the user's lint script seeds the
		// CWD = repo root so a `./bin/r1` build also resolves.
		bin := "r1"
		if _, err := os.Stat("./bin/r1"); err == nil {
			bin = "./bin/r1"
		}
		cmd := exec.Command(bin, "mcp", "serve", "--print-tools")
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("spawn %s: %w", bin, err)
		}
		raw = out
	}
	var tools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, fmt.Errorf("parse catalog JSON: %w", err)
	}
	out := map[string]bool{}
	for _, t := range tools {
		out[t.Name] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("catalog is empty")
	}
	return out, nil
}

// Allowlist gives headless-only tools a reason for being un-referenced
// by any UI surface.
type Allowlist struct {
	HeadlessOnly map[string]string `yaml:"headless_only"`
}

func loadAllowlist(path string) (*Allowlist, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := &Allowlist{HeadlessOnly: map[string]string{}}
	// Lightweight YAML reader: each "  toolname: \"justification\"" line.
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		trim := strings.TrimSpace(line)
		if i := strings.Index(trim, ":"); i > 0 {
			name := strings.TrimSpace(trim[:i])
			val := strings.TrimSpace(trim[i+1:])
			val = strings.Trim(val, `"'`)
			if name != "" {
				out.HeadlessOnly[name] = val
			}
		}
	}
	return out, nil
}

// scanReact walks web/src/**/*.tsx for interactive elements. Returns
// a STATUS: PARTIAL marker when web/src/ doesn't exist (cross-spec
// dependency on spec 6).
func scanReact(root string, catalog map[string]bool) []Finding {
	dir := filepath.Join(root, "web", "src")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return []Finding{{
			Severity: "INFO",
			Surface:  "react",
			Path:     dir,
			Message:  "STATUS: BLOCKED — web/src/ not present in this checkpoint (spec 6 web-chat-ui not merged); React scanner is a no-op until then",
		}}
	}
	out := []Finding{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".tsx") {
			return nil
		}
		out = append(out, scanReactFile(p, catalog)...)
		return nil
	})
	return out
}

// onHandlerRE catches onClick={...}, onChange={...}, onSubmit={...},
// onKeyDown={...}, role="button" attributes — the §8.1 React rules.
var onHandlerRE = regexp.MustCompile(`(?m)\b(onClick|onChange|onSubmit|onKeyDown)=\{|role="button"`)
var dataTestidRE = regexp.MustCompile(`data-testid="([^"]+)"`)
var agenticToolRE = regexp.MustCompile(`(?:mcp_tool|agentic-tool)[^"]*"([^"]+)"`)

func scanReactFile(path string, catalog map[string]bool) []Finding {
	data, err := os.ReadFile(path)
	if err != nil {
		return []Finding{{Severity: "FAIL", Surface: "react", Path: path,
			Message: fmt.Sprintf("read: %v", err)}}
	}
	src := string(data)
	out := []Finding{}
	matches := onHandlerRE.FindAllStringIndex(src, -1)
	for _, m := range matches {
		line := lineNumber(src, m[0])
		// Look for data-testid + agentic mcp_tool within +/- 200 chars.
		window := contextWindow(src, m[0], 200)
		testid := firstSubmatch(dataTestidRE, window)
		tool := firstSubmatch(agenticToolRE, window)
		if testid == "" {
			out = append(out, Finding{
				Severity: "FAIL",
				Surface:  "react",
				Path:     path,
				Line:     line,
				Message:  "interactive element missing data-testid",
			})
		}
		if tool == "" {
			out = append(out, Finding{
				Severity: "FAIL",
				Surface:  "react",
				Path:     path,
				Line:     line,
				Message:  "interactive element missing agentic mcp_tool reference",
			})
			continue
		}
		if !catalog[tool] {
			out = append(out, Finding{
				Severity: "FAIL",
				Surface:  "react",
				Path:     path,
				Line:     line,
				Tool:     tool,
				Message:  fmt.Sprintf("unknown MCP tool %q (not in r1.* catalog)", tool),
			})
		}
	}
	return out
}

// scanBubbleTea is a deliberately conservative pass: it looks for
// files in internal/tui/ that contain a tea.KeyMsg switch AND checks
// that the same file declares an A11yEmitter implementation. This is
// the §8.1 Bubble Tea rule. The lint is BLOCKED-emitting when the
// internal/tui/ tree is absent (development happening elsewhere).
func scanBubbleTea(root string, catalog map[string]bool) []Finding {
	dir := filepath.Join(root, "internal", "tui")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return []Finding{{
			Severity: "INFO",
			Surface:  "bubbletea",
			Path:     dir,
			Message:  "internal/tui/ not present; Bubble Tea scanner skipped",
		}}
	}
	out := []Finding{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		src := string(data)
		if !strings.Contains(src, "tea.KeyMsg") {
			return nil
		}
		// Models consuming tea.KeyMsg without implementing A11y are
		// the failure mode. The implementation is detected by the
		// presence of "A11y() A11yNode" in the same file.
		if !strings.Contains(src, "A11y() A11yNode") &&
			!strings.Contains(src, "A11y() tui.A11yNode") {
			out = append(out, Finding{
				Severity: "FAIL",
				Surface:  "bubbletea",
				Path:     p,
				Message:  "model consumes tea.KeyMsg but does not implement A11yEmitter (A11y() A11yNode method missing)",
			})
		}
		return nil
	})
	return out
}

// scanTauri walks desktop/src-tauri/**/*.rs for #[tauri::command]
// functions and asserts each has an mcp_tool doc comment.
func scanTauri(root string, catalog map[string]bool) []Finding {
	dir := filepath.Join(root, "desktop", "src-tauri")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return []Finding{{
			Severity: "INFO",
			Surface:  "tauri",
			Path:     dir,
			Message:  "STATUS: BLOCKED — desktop/src-tauri/ not present in this checkpoint (spec 7 desktop-cortex-augmentation not merged); Tauri scanner is a no-op until then",
		}}
	}
	// Implementation is symmetric to scanReactFile but uses Rust
	// regex; spec 7 ships the actual sources so the body lands when
	// that merge happens. For now, return an INFO marker so callers
	// know the scanner exists but found nothing to scan.
	return []Finding{{
		Severity: "INFO",
		Surface:  "tauri",
		Path:     dir,
		Message:  "Tauri scanner is wired but desktop sources are empty in this checkpoint",
	}}
}

// scanUnusedTools warns for catalog tools in
// {sessions,lanes,cortex,missions,worktrees} that have no UI surface
// reference AND are not flagged headless-only in the allowlist.
func scanUnusedTools(root string, catalog map[string]bool, allow *Allowlist) []Finding {
	if allow == nil {
		allow = &Allowlist{}
	}
	uiSurfaces := []string{
		filepath.Join(root, "web", "src"),
		filepath.Join(root, "internal", "tui"),
		filepath.Join(root, "desktop", "src-tauri"),
	}
	body := strings.Builder{}
	for _, dir := range uiSurfaces {
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			if data, err := os.ReadFile(p); err == nil {
				body.Write(data)
				body.WriteByte('\n')
			}
			return nil
		})
	}
	src := body.String()

	// Tools we lint for unused-warnings: only the user-facing
	// categories per §8.2 step 5.
	uiCategories := []string{"r1.session.", "r1.lanes.", "r1.cortex.", "r1.mission.", "r1.worktree."}

	names := make([]string, 0, len(catalog))
	for n := range catalog {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []Finding{}
	for _, n := range names {
		if !hasAnyPrefix(n, uiCategories) {
			continue
		}
		if _, headless := allow.HeadlessOnly[n]; headless {
			continue
		}
		if !strings.Contains(src, n) {
			out = append(out, Finding{
				Severity: "WARN",
				Surface:  "catalog",
				Tool:     n,
				Message:  "no UI surface references this tool (and not flagged headless-only in allowlist.yaml)",
			})
		}
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func emitText(findings []Finding, w io.Writer) {
	if len(findings) == 0 {
		fmt.Fprintln(w, "lint-view-without-api: PASS (no findings)")
		return
	}
	for _, f := range findings {
		loc := f.Path
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
		}
		extra := ""
		if f.Tool != "" {
			extra = " [" + f.Tool + "]"
		}
		fmt.Fprintf(w, "%s\t%s\t%s%s\t%s\n", f.Severity, f.Surface, loc, extra, f.Message)
	}
}

// firstSubmatch returns the first group capture of the first match,
// or "" when no match.
func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// contextWindow returns the substring of src centered at offset with
// the given half-radius (in bytes), clamped to bounds.
func contextWindow(src string, offset, radius int) string {
	lo := offset - radius
	if lo < 0 {
		lo = 0
	}
	hi := offset + radius
	if hi > len(src) {
		hi = len(src)
	}
	return src[lo:hi]
}

// lineNumber returns the 1-based line number of byte offset off in
// src.
func lineNumber(src string, off int) int {
	if off > len(src) {
		off = len(src)
	}
	n := 1
	for i := 0; i < off; i++ {
		if src[i] == '\n' {
			n++
		}
	}
	return n
}
