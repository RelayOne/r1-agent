package depcheck

import (
	"errors"
	"io/fs"
	"path/filepath"
)

// skipDirSentinel is a sentinel error visitors use to tell osWalkFunc to
// skip the current directory. We use an internal one rather than
// filepath.SkipDir so depcheck.go doesn't depend on filepath directly.
var skipDirSentinel = errors.New("depcheck: skip dir")

// osWalkFunc walks root and invokes visit(path, isDir) for each entry.
// Returning skipDirSentinel from visit skips the current directory.
func osWalkFunc(root string, visit func(path string, isDir bool) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		isDir := d != nil && d.IsDir()
		if vErr := visit(path, isDir); vErr != nil {
			if errors.Is(vErr, skipDirSentinel) {
				if isDir {
					return filepath.SkipDir
				}
				return nil
			}
			return vErr
		}
		return nil
	})
}

func pathBase(p string) string { return filepath.Base(p) }
