package ideinstall

// vscode.go — VS Code installer (spec §T5). Distinct from Cursor /
// Windsurf in two ways: the root key is `servers` (not `mcpServers`),
// and the per-server stanza adds `"type": "stdio"`.

import (
	"fmt"
	"io"
)

// VSCodeRootKey is the JSON object key under which VS Code expects
// MCP server registrations. The common Cursor/Windsurf key
// `mcpServers` is the wrong key for VS Code; this constant captures
// the difference so reviewers spot the discrepancy at a glance.
const VSCodeRootKey = "servers"

// r1EntryVSCode adds `type: "stdio"` per the VS Code MCP spec — the
// IDE refuses to load entries without an explicit transport.
func r1EntryVSCode() map[string]any {
	return map[string]any{
		"command": "r1",
		"args":    []any{"mcp", "serve"},
		"env":     map[string]any{},
		"type":    "stdio",
	}
}

// InstallVSCode resolves the VS Code config path for the requested
// scope, merges the R1 stanza under the `servers` root key, and
// returns the Result.
func InstallVSCode(scope Scope, workspaceDir string) (Result, error) {
	d := vscodeDetector{}
	path, _, err := d.DetectConfig(scope, workspaceDir)
	if err != nil {
		return Result{}, fmt.Errorf("detect vscode config: %w", err)
	}
	res, err := Merge(MergeOpts{
		Path:     path,
		RootKey:  VSCodeRootKey,
		EntryKey: R1EntryKey,
		Entry:    r1EntryVSCode(),
	})
	if err != nil {
		return res, fmt.Errorf("install vscode: %w", err)
	}
	return res, nil
}

// UninstallVSCode mirrors UninstallCursor: try workspace first (for
// the common case of `r1 ide install vscode --workspace .`), fall
// back to global.
func UninstallVSCode() (Result, error) {
	d := vscodeDetector{}
	if path, _, err := d.DetectConfig(ScopeWorkspace, ""); err == nil {
		if reg, _ := IsRegistered(path, VSCodeRootKey, R1EntryKey); reg || hasBackup(path) {
			return UninstallJSON(IDEVSCode, path, VSCodeRootKey, R1EntryKey)
		}
	}
	path, _, err := d.DetectConfig(ScopeGlobal, "")
	if err != nil {
		return Result{}, fmt.Errorf("detect vscode config: %w", err)
	}
	return UninstallJSON(IDEVSCode, path, VSCodeRootKey, R1EntryKey)
}

// PrintVSCodeConfirmation prints the install confirmation including
// the mandatory Copilot-Agent reminder per spec §T5.2. The reminder
// is non-negotiable: VS Code's MCP subsystem is silent in Copilot
// Chat mode and operators routinely report "tools missing" otherwise.
func PrintVSCodeConfirmation(w io.Writer, res Result) {
	switch res.Action {
	case ActionNoop:
		fmt.Fprintf(w, "no change: r1 already registered in %s\n", res.Path)
	case ActionUpdated:
		fmt.Fprintf(w, "updated: r1 -> %s (backup: %s)\n", res.Path, res.Path+BackupSuffix)
	case ActionAdded:
		fmt.Fprintf(w, "installed: r1 -> %s\n", res.Path)
	}
	fmt.Fprintln(w, "note: VS Code MCP tools are only invoked in Copilot Agent mode;")
	fmt.Fprintln(w, "      switch the Copilot panel to \"Agent\" to use R1.")
}
