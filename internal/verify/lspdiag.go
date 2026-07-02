// lspdiag.go — opt-in edit-time LSP diagnostics verification step.
//
// This is the consumer seam for the multi-language LSP client
// (internal/lsp/client, T-R1P-020), which was library-only until audit
// A067. When enabled (R1_LSP_DIAGNOSTICS=1 — default off), Pipeline.Run
// appends an "lsp" outcome after build/test/lint: it detects the
// worktree's changed files via git, maps them to languages via
// skill.LSPLanguageForFile, launches the matching external language
// server (gopls, pyright-langserver/pylsp, typescript-language-server,
// rust-analyzer), opens each changed document, and fails the step only
// when the server reports Error-severity diagnostics.
//
// Degradation contract: everything that is not an Error-severity
// diagnostic degrades gracefully — a missing language-server binary,
// a non-git directory, a remote execution environment, or an initialize
// failure produces a skipped/annotated outcome, never a verification
// failure.
package verify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/lsp/client"
	"github.com/RelayOne/r1/internal/skill"
)

// lspDiagEnv is the default-off switch for the edit-time LSP
// diagnostics step (audit A067).
const lspDiagEnv = "R1_LSP_DIAGNOSTICS"

// defaultLSPDiagWait bounds how long we wait for server-pushed
// diagnostics per language after opening the changed documents.
const defaultLSPDiagWait = 2 * time.Second

// LSPDiagnosticsEnabled reports whether the opt-in edit-time LSP
// diagnostics verification step is enabled. Default off; set
// R1_LSP_DIAGNOSTICS=1 to enable.
func LSPDiagnosticsEnabled() bool {
	return os.Getenv(lspDiagEnv) == "1"
}

// lspLauncher launches a language server for the given canonical
// language id rooted at root. Production default is
// client.LaunchByLanguage; tests inject an in-memory transport.
type lspLauncher func(lang, root string) (*client.Client, error)

// runLSPDiagnostics executes the LSP diagnostics step for the changed
// files in dir. Returns a single "lsp" outcome.
func (p *Pipeline) runLSPDiagnostics(ctx context.Context, dir string) Outcome {
	if p.environ != nil {
		return Outcome{Name: "lsp", Skipped: true, Success: true,
			Output: "lsp diagnostics run on the host only; skipped in remote execution environments"}
	}

	files, err := changedFiles(ctx, dir)
	if err != nil {
		return Outcome{Name: "lsp", Skipped: true, Success: true,
			Output: fmt.Sprintf("changed-file detection failed (not a git worktree?): %v", err)}
	}

	byLang := make(map[string][]string)
	for _, f := range files {
		if lang, ok := skill.LSPLanguageForFile(f); ok {
			byLang[lang.ID] = append(byLang[lang.ID], f)
		}
	}
	if len(byLang) == 0 {
		return Outcome{Name: "lsp", Skipped: true, Success: true,
			Output: "no changed files map to a supported LSP language"}
	}

	launch := p.lspLaunch
	if launch == nil {
		launch = client.LaunchByLanguage
	}
	wait := p.lspWait
	if wait <= 0 {
		wait = defaultLSPDiagWait
	}

	langs := make([]string, 0, len(byLang))
	for lang := range byLang {
		langs = append(langs, lang)
	}
	sort.Strings(langs)

	var b strings.Builder
	launched := 0
	errCount := 0
	for _, lang := range langs {
		c, err := launch(lang, dir)
		if err != nil {
			// Graceful degradation: a missing PATH binary (or any
			// launch failure) annotates, never fails.
			fmt.Fprintf(&b, "%s: language server unavailable, skipped (%v)\n", lang, err)
			continue
		}
		launched++
		errCount += collectLangDiagnostics(ctx, c, dir, lang, byLang[lang], wait, &b)
	}

	out := strings.TrimSpace(b.String())
	if launched == 0 {
		return Outcome{Name: "lsp", Skipped: true, Success: true, Output: out}
	}
	if errCount > 0 {
		return Outcome{Name: "lsp", Success: false,
			Output: fmt.Sprintf("%d error-severity LSP diagnostic(s)\n%s", errCount, out)}
	}
	if out == "" {
		out = "no error-severity diagnostics"
	}
	return Outcome{Name: "lsp", Success: true, Output: out}
}

// collectLangDiagnostics opens each changed file of one language on the
// launched client and counts Error-severity diagnostics, annotating b.
func collectLangDiagnostics(ctx context.Context, c *client.Client, dir, lang string, files []string, wait time.Duration, b *strings.Builder) int {
	defer func() { _ = c.Shutdown() }()

	if err := c.Initialize(client.PathToURI(dir)); err != nil {
		fmt.Fprintf(b, "%s: initialize failed, skipped (%v)\n", lang, err)
		return 0
	}

	type openDoc struct {
		rel string
		uri string
	}
	var opened []openDoc
	for _, f := range files {
		abs := filepath.Join(dir, f)
		data, err := os.ReadFile(abs) // #nosec G304 -- path comes from git status inside the verified worktree.
		if err != nil {
			// Deleted or unreadable file — nothing to diagnose.
			continue
		}
		uri := client.PathToURI(abs)
		if err := c.OpenDocument(uri, lang, string(data)); err != nil {
			fmt.Fprintf(b, "%s: didOpen %s failed (%v)\n", lang, f, err)
			continue
		}
		opened = append(opened, openDoc{rel: f, uri: uri})
	}

	// Diagnostics are server-pushed (textDocument/publishDiagnostics);
	// poll the client's buffer briefly per document.
	deadline := time.Now().Add(wait)
	errCount := 0
	for _, d := range opened {
		for _, diag := range pollDiagnostics(ctx, c, d.uri, deadline) {
			if diag.Severity == 1 { // 1=Error per LSP 3.17
				errCount++
				fmt.Fprintf(b, "%s:%d:%d: %s\n", d.rel, diag.Range.Start.Line+1, diag.Range.Start.Character+1, diag.Message)
			}
		}
	}
	return errCount
}

// pollDiagnostics polls the client's buffered diagnostics for uri until
// they arrive, the deadline passes, or ctx is cancelled.
func pollDiagnostics(ctx context.Context, c *client.Client, uri string, deadline time.Time) []client.Diagnostic {
	for {
		diags, err := c.Diagnostics(uri)
		if err == nil && len(diags) > 0 {
			return diags
		}
		if time.Now().After(deadline) {
			return diags
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// changedFiles lists the worktree's modified + untracked files relative
// to dir via `git status --porcelain`.
func changedFiles(ctx context.Context, dir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if i := strings.Index(path, " -> "); i >= 0 { // rename: keep new side
			path = path[i+4:]
		}
		path = strings.Trim(path, `"`)
		if path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}
