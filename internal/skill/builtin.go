package skill

import (
	"embed"
	"io/fs"
	"path/filepath"
	"strings"
)

//go:embed builtin/*.md
var builtinFS embed.FS

// LoadBuiltins loads the embedded built-in skill files into the registry.
// Built-in skills have lower priority than project and user skills.
func (r *Registry) LoadBuiltins() error {
	return fs.WalkDir(builtinFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		content, err := fs.ReadFile(builtinFS, path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(path), ".md")

		r.mu.Lock()
		// Don't overwrite project/user skills
		if _, exists := r.skills[name]; !exists {
			r.skills[name] = parseSkill(name, string(content), "builtin", "embedded://"+path, 0)
		}
		r.mu.Unlock()
		return nil
	})
}

// BuiltinCount returns the number of embedded built-in skills.
func BuiltinCount() int {
	count := 0
	fs.WalkDir(builtinFS, "builtin", func(path string, d fs.DirEntry, _ error) error {
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			count++
		}
		return nil
	})
	return count
}
