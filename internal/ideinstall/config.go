// Package ideinstall implements the shared installer plumbing for
// `r1 ide install <ide>` — see specs/mcp-ide-bundles.md (C4).
//
// The package exposes:
//
//   - A Detector interface implemented per-IDE (cursor, windsurf,
//     vscode, jetbrains) covering config-path resolution.
//   - A JSON merge primitive (Merge) shared by every JSON-config IDE
//     (Cursor, Windsurf, VS Code). The primitive owns the read →
//     parse → backup → atomic-write contract.
//   - Per-IDE install/uninstall entry points wrapping the primitive
//     with the right path + root key + R1 stanza.
//   - A Verify report returning one VerifyResult per IDE.
//
// JetBrains is the outlier: it ships a `.jar` instead of editing JSON,
// so jetbrains.go bypasses Merge and calls Copy directly. Everything
// else funnels through this file.
package ideinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

// BackupSuffix is appended to the original config-file path when we
// take a backup before merging. Standardised across every IDE so an
// operator can recognise R1 backups regardless of source.
const BackupSuffix = ".r1-backup"

// JarName is the bundled JetBrains plugin jar dropped into the
// per-IDE plugins directory by `r1 ide install jetbrains`.
const JarName = "r1-mcp-bridge.jar"

// Scope is the install scope — workspace-local or global (user-level).
type Scope string

const (
	// ScopeWorkspace targets the IDE's workspace-scoped config file
	// (e.g. ./.cursor/mcp.json). Cursor + VS Code default to this.
	ScopeWorkspace Scope = "workspace"
	// ScopeGlobal targets the IDE's user-level config (e.g.
	// ~/.cursor/mcp.json). Windsurf + JetBrains support only this.
	ScopeGlobal Scope = "global"
)

// Action describes what a Merge / Uninstall call did to the target
// file. Surfaced via Result so callers can render human-readable
// confirmation lines.
type Action string

const (
	ActionAdded    Action = "added"
	ActionUpdated  Action = "updated"
	ActionNoop     Action = "noop"
	ActionRestored Action = "restored"
	ActionRemoved  Action = "removed"
)

// Result is returned by every install / uninstall call. Path is the
// final destination on disk; Action describes the mutation.
type Result struct {
	Path   string
	Action Action
}

// MergeOpts parameterises Merge. Path is the final destination (the
// installer caller resolves the IDE-specific path). RootKey is the
// JSON object key under which Entry is inserted (mcpServers for
// Cursor/Windsurf, servers for VS Code). EntryKey is the key under
// RootKey ("r1" for every R1 install). Entry is the value associated
// with EntryKey (the {command, args, env[, type]} object).
type MergeOpts struct {
	Path     string
	RootKey  string
	EntryKey string
	Entry    map[string]any
}

// Merge implements the shared read → parse → backup → atomic-write
// flow per D-1, D-2, D-3 of specs/mcp-ide-bundles.md. Behavior:
//
//   - If the file does not exist, its parent dir is created and a
//     fresh {RootKey: {EntryKey: Entry}} document is written. No
//     backup is written (there's nothing to back up).
//   - If the file exists, it is parsed as a JSON object. The RootKey
//     is created if absent (preserving every other top-level key).
//     Under RootKey, EntryKey is upserted with Entry. If the existing
//     value is deep-equal to Entry, ResultNoop is returned without
//     touching disk.
//   - On every non-noop write, the original file (if any) is copied
//     verbatim to `<Path>.r1-backup` before the merged document is
//     written atomically (write <tmp> → rename).
func Merge(opts MergeOpts) (Result, error) {
	if opts.Path == "" {
		return Result{}, errors.New("ideinstall.Merge: empty Path")
	}
	if opts.RootKey == "" {
		return Result{}, errors.New("ideinstall.Merge: empty RootKey")
	}
	if opts.EntryKey == "" {
		return Result{}, errors.New("ideinstall.Merge: empty EntryKey")
	}
	if opts.Entry == nil {
		return Result{}, errors.New("ideinstall.Merge: nil Entry")
	}

	res := Result{Path: opts.Path}

	// Read existing config (if any).
	raw, readErr := os.ReadFile(opts.Path)
	var existed bool
	switch {
	case readErr == nil:
		existed = true
	case errors.Is(readErr, os.ErrNotExist):
		existed = false
	default:
		return res, fmt.Errorf("read config %q: %w", opts.Path, readErr)
	}

	// Parse (or seed) the document.
	doc := map[string]any{}
	if existed && len(raw) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return res, fmt.Errorf("parse config %q: %w", opts.Path, err)
		}
	}
	rootRaw, hasRoot := doc[opts.RootKey]
	rootMap, _ := rootRaw.(map[string]any)
	if !hasRoot || rootMap == nil {
		rootMap = map[string]any{}
	}

	// Diff against existing entry to decide Action.
	existing, hasEntry := rootMap[opts.EntryKey]
	switch {
	case hasEntry && reflect.DeepEqual(existing, mapifyEntry(opts.Entry)):
		res.Action = ActionNoop
		return res, nil
	case hasEntry:
		res.Action = ActionUpdated
	default:
		res.Action = ActionAdded
	}

	// Materialise the merged document.
	rootMap[opts.EntryKey] = opts.Entry
	doc[opts.RootKey] = rootMap
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return res, fmt.Errorf("encode merged config: %w", err)
	}

	// Backup the original (if any) before writing.
	if existed {
		if err := writeAtomic(opts.Path+BackupSuffix, raw); err != nil {
			return res, fmt.Errorf("write backup %q: %w", opts.Path+BackupSuffix, err)
		}
	}

	// Ensure parent dir exists. New users may invoke install before
	// the IDE has created its config dir.
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o755); err != nil {
		return res, fmt.Errorf("create parent dir for %q: %w", opts.Path, err)
	}

	if err := writeAtomic(opts.Path, encoded); err != nil {
		return res, fmt.Errorf("write config %q: %w", opts.Path, err)
	}
	return res, nil
}

// mapifyEntry round-trips a `map[string]any` through json.Marshal +
// json.Unmarshal so reflect.DeepEqual sees the same types on both
// sides (json.Unmarshal turns numbers into float64, nested objects
// into map[string]any, etc.). Without this round-trip, a literal
// `map[string]any{"args": []string{"mcp","serve"}}` would never
// deep-equal a re-parsed `[]any{"mcp","serve"}` from disk.
func mapifyEntry(in map[string]any) map[string]any {
	b, err := json.Marshal(in)
	if err != nil {
		return in
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return in
	}
	return out
}

// writeAtomic writes data to a temp file in the same directory then
// os.Renames it onto dest. Cross-FS renames on Linux fall back to the
// caller-visible error — both callers in this package only write into
// the same FS as the destination so the fallback is unreachable in
// practice.
func writeAtomic(dest string, data []byte) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(dest)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		cleanup()
		return err
	}
	return nil
}

// UninstallJSON removes the EntryKey from the IDE's JSON config and
// returns the result. Restore-from-backup is attempted first because
// backups capture the IDE's pristine state including formatting; if
// no backup is present we mutate the existing file in place.
//
// `id` is purely an error-message tag (the IDE name) so the caller
// gets `r1 is not installed in cursor` instead of a generic message.
func UninstallJSON(id, path, rootKey, entryKey string) (Result, error) {
	res := Result{Path: path}
	backup := path + BackupSuffix
	if _, err := os.Stat(backup); err == nil {
		if err := os.Rename(backup, path); err != nil {
			return res, fmt.Errorf("restore backup %q: %w", backup, err)
		}
		res.Action = ActionRestored
		return res, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return res, fmt.Errorf("r1 is not installed in %s", id)
		}
		return res, fmt.Errorf("read config %q: %w", path, err)
	}
	doc := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return res, fmt.Errorf("parse config %q: %w", path, err)
		}
	}
	rootRaw, ok := doc[rootKey]
	rootMap, _ := rootRaw.(map[string]any)
	if !ok || rootMap == nil {
		return res, fmt.Errorf("r1 is not installed in %s", id)
	}
	if _, has := rootMap[entryKey]; !has {
		return res, fmt.Errorf("r1 is not installed in %s", id)
	}
	delete(rootMap, entryKey)
	doc[rootKey] = rootMap
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return res, fmt.Errorf("encode config: %w", err)
	}
	if err := writeAtomic(path, encoded); err != nil {
		return res, fmt.Errorf("write config %q: %w", path, err)
	}
	res.Action = ActionRemoved
	return res, nil
}

// IsRegistered returns true when the IDE's JSON config contains an
// entry under rootKey/entryKey. Used by Verify and by the first-run
// prompt to decide which IDE is unregistered.
func IsRegistered(path, rootKey, entryKey string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if len(raw) == 0 {
		return false, nil
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("parse config %q: %w", path, err)
	}
	rootRaw, ok := doc[rootKey]
	if !ok {
		return false, nil
	}
	rootMap, ok := rootRaw.(map[string]any)
	if !ok {
		return false, nil
	}
	_, has := rootMap[entryKey]
	return has, nil
}

// copyFile copies src to dst atomically (write tmp + rename).
// Used by jetbrains.go for the jar copy; lives here so install +
// uninstall share one atomic-write primitive.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		cleanup()
		return err
	}
	return nil
}
