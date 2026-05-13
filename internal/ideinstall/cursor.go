package ideinstall

// cursor.go — Cursor installer (spec §T3).

import (
	"fmt"
	"io"
)

// CursorRootKey is the JSON object key under which Cursor expects MCP
// server registrations. Shared with Windsurf (same schema).
const CursorRootKey = "mcpServers"

// R1EntryKey is the key under the root object used for the R1
// registration in every JSON-config IDE.
const R1EntryKey = "r1"

// r1EntryCursor returns the R1 stanza for Cursor / Windsurf
// (no `type` field — the Cursor MCP UI rejects unknown keys silently).
func r1EntryCursor() map[string]any {
	return map[string]any{
		"command": "r1",
		"args":    []any{"mcp", "serve"},
		"env":     map[string]any{},
	}
}

// InstallCursor resolves the Cursor config path for the requested
// scope, merges the R1 stanza, and returns the Result. Errors are
// wrapped with enough context that the CLI layer can surface them
// without re-decorating.
func InstallCursor(scope Scope, workspaceDir string) (Result, error) {
	d := cursorDetector{}
	path, _, err := d.DetectConfig(scope, workspaceDir)
	if err != nil {
		return Result{}, fmt.Errorf("detect cursor config: %w", err)
	}
	res, err := Merge(MergeOpts{
		Path:     path,
		RootKey:  CursorRootKey,
		EntryKey: R1EntryKey,
		Entry:    r1EntryCursor(),
	})
	if err != nil {
		return res, fmt.Errorf("install cursor: %w", err)
	}
	return res, nil
}

// UninstallCursor restores the pre-install backup if one exists or
// drops the `r1` entry from `mcpServers`. Returns `r1 is not
// installed in cursor` if neither path applies.
func UninstallCursor() (Result, error) {
	d := cursorDetector{}
	// Try both scopes: workspace first (current dir), then global.
	// This mirrors the install default and avoids forcing the
	// operator to remember which scope they used.
	if path, _, err := d.DetectConfig(ScopeWorkspace, ""); err == nil {
		if reg, _ := IsRegistered(path, CursorRootKey, R1EntryKey); reg || hasBackup(path) {
			return UninstallJSON(IDECursor, path, CursorRootKey, R1EntryKey)
		}
	}
	path, _, err := d.DetectConfig(ScopeGlobal, "")
	if err != nil {
		return Result{}, fmt.Errorf("detect cursor config: %w", err)
	}
	return UninstallJSON(IDECursor, path, CursorRootKey, R1EntryKey)
}

// hasBackup is a small helper used by UninstallCursor / UninstallVSCode
// to decide whether the workspace-scope path is the right uninstall
// target. We accept either a live registration or a backup as proof.
func hasBackup(p string) bool {
	return fileExists(p + BackupSuffix)
}

// PrintCursorConfirmation writes the post-install confirmation lines
// to the supplied writer. CLI surface; kept in the package so unit
// tests can assert the exact text without importing main.
func PrintCursorConfirmation(w io.Writer, res Result) {
	switch res.Action {
	case ActionNoop:
		fmt.Fprintf(w, "no change: r1 already registered in %s\n", res.Path)
	case ActionUpdated:
		fmt.Fprintf(w, "updated: r1 -> %s (backup: %s)\n", res.Path, res.Path+BackupSuffix)
	case ActionAdded:
		fmt.Fprintf(w, "installed: r1 -> %s\n", res.Path)
	}
	fmt.Fprintln(w, "note: open Cursor and toggle the MCP panel; r1 should appear in the list.")
}
