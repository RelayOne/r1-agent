//go:build !unix

package patchapply

// isHardLinked is a no-op on platforms without hard-link stat support;
// atomic temp+rename is used everywhere there.
func isHardLinked(path string) bool { return false }
