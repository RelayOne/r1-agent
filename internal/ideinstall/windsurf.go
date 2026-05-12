package ideinstall

// windsurf.go — Windsurf installer (spec §T4). Windsurf is global-
// only, so the public entry takes no scope argument.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// r1EntryWindsurf reuses the Cursor stanza — Windsurf accepts the
// same {command, args, env} shape per the research note in the spec.
func r1EntryWindsurf() map[string]any {
	return r1EntryCursor()
}

// InstallWindsurf is the global-only Windsurf installer. Always uses
// ScopeGlobal because Windsurf has no workspace-scope config.
//
// The Windsurf parent dir (`~/.codeium/windsurf/`) is rarely present
// before R1 runs because Windsurf only creates it after first launch.
// We MkdirAll it and warn on stderr if `~/.codeium/` is also absent
// (operator might have a fresh OS or no Windsurf install at all).
func InstallWindsurf(stderr io.Writer) (Result, error) {
	d := windsurfDetector{}
	path, _, err := d.DetectConfig(ScopeGlobal, "")
	if err != nil {
		return Result{}, fmt.Errorf("detect windsurf config: %w", err)
	}
	parent := filepath.Dir(path)
	grandparent := filepath.Dir(parent) // ~/.codeium
	if _, err := os.Stat(grandparent); os.IsNotExist(err) && stderr != nil {
		fmt.Fprintf(stderr,
			"warning: %s did not exist; Windsurf may not be installed\n",
			grandparent)
	}
	res, err := Merge(MergeOpts{
		Path:     path,
		RootKey:  CursorRootKey,
		EntryKey: R1EntryKey,
		Entry:    r1EntryWindsurf(),
	})
	if err != nil {
		return res, fmt.Errorf("install windsurf: %w", err)
	}
	return res, nil
}

// UninstallWindsurf delegates to UninstallJSON against the global
// config path.
func UninstallWindsurf() (Result, error) {
	d := windsurfDetector{}
	path, _, err := d.DetectConfig(ScopeGlobal, "")
	if err != nil {
		return Result{}, fmt.Errorf("detect windsurf config: %w", err)
	}
	return UninstallJSON(IDEWindsurf, path, CursorRootKey, R1EntryKey)
}

// PrintWindsurfConfirmation writes the install confirmation. The text
// is intentionally similar to the Cursor variant so docs can lift one
// snippet for both IDEs.
func PrintWindsurfConfirmation(w io.Writer, res Result) {
	switch res.Action {
	case ActionNoop:
		fmt.Fprintf(w, "no change: r1 already registered in %s\n", res.Path)
	case ActionUpdated:
		fmt.Fprintf(w, "updated: r1 -> %s (backup: %s)\n", res.Path, res.Path+BackupSuffix)
	case ActionAdded:
		fmt.Fprintf(w, "installed: r1 -> %s\n", res.Path)
	}
	fmt.Fprintln(w, "note: open Windsurf and the Cascade panel lists r1 under MCP servers.")
}

// fileExists is a tiny helper shared with cursor.go.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
