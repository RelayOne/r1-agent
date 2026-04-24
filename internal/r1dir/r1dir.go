// Package r1dir implements the `.stoke/` → `.r1/` per-project data-dir
// dual-resolve window per work-r1-rename.md §S1-5.
//
// The directive:
//
//   - READ from both `.r1/` (canonical) and `.stoke/` (legacy), preferring
//     `.r1/` when both exist.
//   - WRITE to BOTH `.r1/<rel>` AND `.stoke/<rel>` during the transition
//     so operators and external scripts that still consume the legacy
//     layout keep working, and so rollback to pre-rename builds only
//     requires deleting `.r1/` — never recreating lost state under
//     `.stoke/`.
//
// The window is indefinite per the spec: legacy `.stoke/` remains readable
// (and writable via dual-write) until operators run the opt-in
// `r1 migrate-session` tool. No forced drop date.
//
// This package is intentionally additive: existing hardcoded `".stoke/"`
// call sites keep working unchanged. Callers that opt in to the helper
// gain read-fallback + dual-write, at the cost of two file writes per
// WriteFile. A full sweep of every hardcoded literal lands in a follow-up;
// this package + a starter batch of high-traffic call sites is Phase S1-5.
//
// # API surface
//
//	Root() string            // resolved root: ".r1" if present under cwd, else ".stoke"
//	RootFor(repo string)     // same, but rooted at an explicit repo path
//	Join(parts ...string)    // filepath.Join with the resolved root
//	CanonicalPath(rel)       // always ".r1/<rel>"
//	LegacyPath(rel)          // always ".stoke/<rel>"
//	CanonicalPathFor(repo, rel)  // <repo>/.r1/<rel>
//	LegacyPathFor(repo, rel)     // <repo>/.stoke/<rel>
//	ReadFile(rel) ([]byte, error)          // try .r1 first, fall back to .stoke
//	ReadFileFor(repo, rel) ([]byte, error) // same, rooted at repo
//	WriteFile(rel, data, perm) error       // write to BOTH .r1 and .stoke
//	WriteFileFor(repo, rel, data, perm) error
//
// The directory-name constants are exported so call sites that need the
// literal ".r1" / ".stoke" tokens (e.g. in comments or hook rule lists)
// can reference them without drift.
package r1dir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Canonical is the canonical per-project data-dir name. Post-rename all
// new state writes land here.
const Canonical = ".r1"

// Legacy is the pre-rename data-dir name. Reads fall back to it when the
// canonical dir is absent; writes tee into it until the migration window
// closes.
const Legacy = ".stoke"

// Root returns the resolved per-project data-dir name, evaluated against
// the current working directory. If `.r1` exists it wins; otherwise the
// helper returns `.stoke` so legacy sessions keep resolving. Callers that
// have a known repo root should prefer RootFor for determinism under
// `cmd.Dir`-style invocations.
//
// The returned value is a bare directory name (not an absolute path), so
// it composes with filepath.Join(repoRoot, Root(), ...) without surprising
// a caller that mixed relative and absolute paths.
func Root() string {
	return RootFor(".")
}

// RootFor returns the resolved per-project data-dir name, evaluated
// against the given repository root. Same preference rules as Root.
func RootFor(repo string) string {
	if repo == "" {
		repo = "."
	}
	canonical := filepath.Join(repo, Canonical)
	if info, err := os.Stat(canonical); err == nil && info.IsDir() {
		return Canonical
	}
	return Legacy
}

// Join joins parts onto the resolved root under the current working
// directory. Equivalent to filepath.Join(Root(), parts...).
func Join(parts ...string) string {
	args := make([]string, 0, len(parts)+1)
	args = append(args, Root())
	args = append(args, parts...)
	return filepath.Join(args...)
}

// JoinFor joins parts onto the resolved root under repo. Equivalent to
// filepath.Join(repo, RootFor(repo), parts...).
func JoinFor(repo string, parts ...string) string {
	args := make([]string, 0, len(parts)+2)
	args = append(args, repo)
	args = append(args, RootFor(repo))
	args = append(args, parts...)
	return filepath.Join(args...)
}

// CanonicalPath returns the always-canonical path (cwd-relative). Use
// when a caller must write to the canonical layout regardless of what
// exists on disk (e.g. inspectors reporting "where will this new file
// land").
func CanonicalPath(rel string) string {
	return filepath.Join(Canonical, rel)
}

// CanonicalPathFor returns the always-canonical path rooted at repo.
func CanonicalPathFor(repo, rel string) string {
	return filepath.Join(repo, Canonical, rel)
}

// LegacyPath returns the always-legacy path (cwd-relative). Use when a
// caller must reference the pre-rename layout explicitly, e.g. cleanup
// tools or diagnostic output.
func LegacyPath(rel string) string {
	return filepath.Join(Legacy, rel)
}

// LegacyPathFor returns the always-legacy path rooted at repo.
func LegacyPathFor(repo, rel string) string {
	return filepath.Join(repo, Legacy, rel)
}

// ReadFile reads rel from the resolved root, preferring .r1/ over .stoke/.
// Returns os.ErrNotExist (wrapped) when neither path has the file.
func ReadFile(rel string) ([]byte, error) {
	return ReadFileFor(".", rel)
}

// ReadFileFor reads rel rooted at repo, preferring .r1/<rel> over
// .stoke/<rel>. When .r1/<rel> exists but returns a non-IsNotExist error
// (e.g. a permission error), that error is returned verbatim; the helper
// does NOT silently fall through to legacy in that case — failing fast
// on real I/O errors is less surprising than swallowing them.
func ReadFileFor(repo, rel string) ([]byte, error) {
	canonical := filepath.Join(repo, Canonical, rel)
	data, err := os.ReadFile(canonical)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	legacy := filepath.Join(repo, Legacy, rel)
	data, err = os.ReadFile(legacy)
	if err == nil {
		return data, nil
	}
	// Normalize the error to point at the canonical path so callers see
	// the post-rename path in their "file not found" messages. The
	// underlying fs.ErrNotExist is preserved via os.PathError wrapping.
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &os.PathError{Op: "open", Path: canonical, Err: fs.ErrNotExist}
	}
	return nil, err
}

// WriteFile writes data to BOTH .r1/<rel> and .stoke/<rel> at perm,
// creating parent directories as needed. Returns the first error
// encountered; when the canonical write succeeds but the legacy write
// fails (or vice versa), the helper stops and surfaces that error — it
// does NOT attempt to roll back the successful write, because a partial
// write of identical bytes to one side is not harmful (the next reader
// still sees the expected content via the read-fallback rule).
//
// The perm is applied to both files. Parent directories are created with
// 0o700 to match existing session/ledger conventions.
func WriteFile(rel string, data []byte, perm fs.FileMode) error {
	return WriteFileFor(".", rel, data, perm)
}

// WriteFileFor writes data to <repo>/.r1/<rel> and <repo>/.stoke/<rel>.
// See WriteFile for semantics.
func WriteFileFor(repo, rel string, data []byte, perm fs.FileMode) error {
	canonical := filepath.Join(repo, Canonical, rel)
	legacy := filepath.Join(repo, Legacy, rel)
	if err := writeOne(canonical, data, perm); err != nil {
		return err
	}
	return writeOne(legacy, data, perm)
}

// writeOne creates parent directories (0o700) then writes the file with
// perm. Extracted for symmetry between canonical and legacy writes.
func writeOne(path string, data []byte, perm fs.FileMode) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, perm)
}
