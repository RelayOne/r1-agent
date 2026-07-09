package acp

import (
	"os"
	"path/filepath"
)

// writeFile writes name (relative to dir) with the given content.
func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}
