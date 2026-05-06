// Copyright (c) 2026 Good Ventures.
// SPDX-License-Identifier: Apache-2.0

package r1rename

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/RelayOne/r1/internal/r1dir"
)

// Data-dir dual-resolve window per work-r1-rename.md S1-5. The window
// is INDEFINITE: legacy .stoke/ remains readable until operators run
// the opt-in r1 migrate-session tool. The bulk read/write/resolve
// helpers live in internal/r1dir; the helpers below re-export the
// constants and add the one-shot copy operation the work-order
// specifies.

const (
	// CanonicalDataDir is the post-rename per-project data-dir name
	// (".r1"). Re-exported from internal/r1dir so callers that only
	// need the constant don't have to add a second import.
	CanonicalDataDir = r1dir.Canonical
	// LegacyDataDir is the pre-rename data-dir name (".stoke"). Reads
	// fall back to it when the canonical dir is absent.
	LegacyDataDir = r1dir.Legacy
)

// MigrationManifestName is written into the canonical .r1/ root when
// MigrateStokeDir completes. Its presence flags the migration as done
// and is used by future migration runs to skip already-migrated trees.
const MigrationManifestName = "MIGRATED-FROM-STOKE.txt"

// MigrateStokeDir copies the legacy .stoke/ tree under repo into a
// new .r1/ tree, leaving the legacy tree intact for rollback.
//
// Semantics:
//
//   - If the legacy tree is absent, MigrateStokeDir returns
//     fs.ErrNotExist (so callers can branch cleanly on
//     errors.Is(err, fs.ErrNotExist)).
//   - If the canonical .r1/ tree already exists AND already carries a
//     MIGRATED-FROM-STOKE.txt manifest, MigrateStokeDir returns nil
//     (idempotent re-run).
//   - If the canonical tree exists but has no manifest, the helper
//     returns ErrCanonicalDirExists rather than overwriting -- callers
//     resolve manually (the operator-driven `r1 migrate-session` CLI
//     surfaces this as an actionable error).
//   - Otherwise, every file under the legacy tree is copied to the
//     canonical tree, preserving relative paths and file modes; a
//     MIGRATED-FROM-STOKE.txt manifest is written into the canonical
//     root.
//
// The operation is opt-in (per the work-order) -- nothing in the
// runtime calls this implicitly. The CLI helper `r1 migrate-session`
// is the user-facing entry point and is wired into cmd/r1 in the
// follow-up; this function ships the contract.
//
// repo == "" is treated as ".".
func MigrateStokeDir(repo string) error {
	if repo == "" {
		repo = "."
	}
	legacy := filepath.Join(repo, r1dir.Legacy)
	canonical := filepath.Join(repo, r1dir.Canonical)

	info, err := os.Stat(legacy)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("r1rename: legacy data dir %q: %w", legacy, fs.ErrNotExist)
		}
		return fmt.Errorf("r1rename: stat legacy data dir %q: %w", legacy, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("r1rename: legacy path %q is not a directory", legacy)
	}

	if cinfo, cerr := os.Stat(canonical); cerr == nil && cinfo.IsDir() {
		manifest := filepath.Join(canonical, MigrationManifestName)
		if _, merr := os.Stat(manifest); merr == nil {
			// Idempotent: already migrated.
			return nil
		}
		return fmt.Errorf("r1rename: canonical data dir %q already exists without migration manifest: %w", canonical, ErrCanonicalDirExists)
	} else if cerr != nil && !errors.Is(cerr, fs.ErrNotExist) {
		return fmt.Errorf("r1rename: stat canonical data dir %q: %w", canonical, cerr)
	}

	if err := copyTree(legacy, canonical); err != nil {
		return fmt.Errorf("r1rename: copy %q -> %q: %w", legacy, canonical, err)
	}
	manifest := filepath.Join(canonical, MigrationManifestName)
	body := []byte(fmt.Sprintf("Migrated from %s on %s.\nLegacy tree preserved for rollback.\n", legacy, EnvDeprecationDate))
	if err := os.WriteFile(manifest, body, 0o600); err != nil {
		return fmt.Errorf("r1rename: write migration manifest %q: %w", manifest, err)
	}
	return nil
}

// ErrCanonicalDirExists indicates MigrateStokeDir found a canonical
// .r1/ tree that was NOT created by a previous migration run, so it
// refused to overwrite. Callers should surface this to the operator
// rather than ignore it.
var ErrCanonicalDirExists = errors.New("canonical .r1/ already exists without migration manifest")

// copyTree mirrors src to dst, preserving relative paths, file modes,
// and directory permissions (0o700 for created dirs to match the
// existing session/ledger conventions). Symlinks are NOT followed --
// they are recreated as symlinks at the destination, mirroring the
// behaviour an operator would expect from `cp -a`. Special files
// (devices, pipes) are skipped with a returned error so the operator
// gets an explicit signal rather than a partial migration.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type()&fs.ModeSymlink != 0:
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		case d.Type().IsRegular():
			return copyFile(path, target)
		default:
			return fmt.Errorf("r1rename: refusing to migrate non-regular non-symlink entry %q (mode %s)", path, d.Type())
		}
	})
}

// copyFile copies src -> dst preserving the source file mode. Parent
// directories are created with 0o700 to match writeOne in r1dir.
func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src) // #nosec G304 -- src is bounded to the legacy data tree under repo.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm()) // #nosec G304 -- dst is bounded to the canonical data tree under repo.
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
