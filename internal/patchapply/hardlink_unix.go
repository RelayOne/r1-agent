//go:build unix

package patchapply

import (
	"os"
	"syscall"
)

// isHardLinked reports whether path is a regular file with more than one
// hard link. Such files must be updated in place (not via temp+rename),
// which would break the link group.
func isHardLinked(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return false // symlinks are resolved before this call
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Nlink > 1
	}
	return false
}
