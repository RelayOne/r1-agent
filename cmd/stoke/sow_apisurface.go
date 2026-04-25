package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RelayOne/r1-agent/internal/logging"
)

// sowAPISurface scans the repo for source files and extracts the public API
// surface (signatures, type defs, exports). This is the missing context that
// makes abstract per-session descriptions buildable: when session N+1 says
// "implement update_concern_field", the model needs to see the ConcernField
// type that session N defined. RepoMap only gives file paths; the actual
// signatures aren't exposed anywhere in the prompt without this.
//
// The output is a single string ready to inject into the system prompt,
// bounded by budget characters.
func sowAPISurface(repoRoot string, budget int) string {
	if repoRoot == "" || budget <= 0 {
		return ""
	}

	type fileAPI struct {
		path  string
		lines []string
	}
	var files []fileAPI

	_ = filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Best-effort scan: log the walk error but continue so a
			// single unreadable path doesn't kill the whole surface.
			logging.Global().Warn("sowAPISurface: walk error", "path", path, "err", walkErr)
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == "target" || name == "node_modules" || name == "vendor" ||
				name == "dist" || name == "build" || name == ".git" ||
				(strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(name)
		switch ext {
		case ".rs", ".go", ".ts", ".tsx", ".js", ".jsx", ".py":
		default:
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			// Best-effort: log unreadable files but keep scanning.
			logging.Global().Warn("sowAPISurface: unreadable file", "path", path, "err", readErr)
			return nil
		}
		if len(content) > 64*1024 {
			content = content[:64*1024]
		}
		api := extractAPILines(ext, string(content))
		if len(api) > 0 {
			files = append(files, fileAPI{path: rel, lines: api})
		}
		return nil
	})

	if len(files) == 0 {
		return ""
	}

	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	var sb strings.Builder
	sb.WriteString("EXISTING CODE SURFACE (read-only context — these definitions already exist; USE them, do not redefine):\n")
	for _, f := range files {
		section := fmt.Sprintf("\n--- %s ---\n", f.path)
		for _, line := range f.lines {
			section += line + "\n"
		}
		if sb.Len()+len(section) > budget {
			sb.WriteString("\n... (truncated to fit budget — read remaining files directly with read_file)\n")
			break
		}
		sb.WriteString(section)
	}
	sb.WriteString("\n")
	return sb.String()
}

// extractAPILines pulls public-API lines out of a source file.
func extractAPILines(ext, content string) []string {
	var out []string
	lines := strings.Split(content, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		stripped := strings.TrimLeft(line, " \t")
		if stripped == "" || strings.HasPrefix(stripped, "//") {
			continue
		}
		if ext == ".py" && strings.HasPrefix(stripped, "#") {
			continue
		}

		var keep bool
		switch ext {
		case ".rs":
			keep = strings.HasPrefix(stripped, "pub fn ") ||
				strings.HasPrefix(stripped, "pub async fn ") ||
				strings.HasPrefix(stripped, "pub struct ") ||
				strings.HasPrefix(stripped, "pub enum ") ||
				strings.HasPrefix(stripped, "pub trait ") ||
				strings.HasPrefix(stripped, "pub type ") ||
				strings.HasPrefix(stripped, "pub const ") ||
				strings.HasPrefix(stripped, "pub use ") ||
				strings.HasPrefix(stripped, "pub mod ") ||
				(strings.HasPrefix(stripped, "impl ") && strings.Contains(stripped, " for ")) ||
				strings.HasPrefix(stripped, "#[derive(")
		case ".go":
			keep = (strings.HasPrefix(stripped, "func ") || strings.HasPrefix(stripped, "type ") ||
				strings.HasPrefix(stripped, "const ") || strings.HasPrefix(stripped, "var ")) &&
				goExportedName(stripped)
		case ".ts", ".tsx", ".js", ".jsx":
			keep = strings.HasPrefix(stripped, "export ") ||
				strings.HasPrefix(stripped, "interface ") ||
				strings.HasPrefix(stripped, "class ") ||
				strings.HasPrefix(stripped, "type ") ||
				strings.HasPrefix(stripped, "enum ")
		case ".py":
			keep = strings.HasPrefix(stripped, "def ") ||
				strings.HasPrefix(stripped, "async def ") ||
				strings.HasPrefix(stripped, "class ")
		}

		if keep {
			out = append(out, stripped)
			if needsContinuationLines(ext, stripped) {
				for j := i + 1; j < len(lines) && j < i+15; j++ {
					next := strings.TrimRight(lines[j], "\r")
					out = append(out, next)
					if strings.Contains(next, "}") {
						break
					}
				}
			}
		}
	}
	return out
}

func goExportedName(line string) bool {
	parts := strings.Fields(line)
	for i := 0; i < len(parts)-1; i++ {
		switch parts[i] {
		case "func", "type", "const", "var":
			next := parts[i+1]
			if next == "(" || strings.HasPrefix(next, "(") {
				if i+3 < len(parts) {
					next = parts[i+3]
				}
			}
			if next != "" && next[0] >= 'A' && next[0] <= 'Z' {
				return true
			}
		}
	}
	return false
}

func needsContinuationLines(ext, line string) bool {
	switch ext {
	case ".rs":
		return strings.HasPrefix(line, "pub struct ") ||
			strings.HasPrefix(line, "pub enum ") ||
			strings.HasPrefix(line, "pub trait ")
	case ".go":
		return strings.HasPrefix(line, "type ") && strings.Contains(line, "struct {")
	}
	return false
}
