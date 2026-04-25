package honesty

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1-agent/internal/hub"
)

// ImportChecker is a gate subscriber that scans newly added imports against
// package registries to catch hallucinated package names.
type ImportChecker struct {
	httpClient *http.Client
	cache      sync.Map // map[string]bool
}

// NewImportChecker creates a new import checker.
func NewImportChecker() *ImportChecker {
	return &ImportChecker{
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// Register adds the import checker to the hub.
func (ic *ImportChecker) Register(bus *hub.Bus) {
	bus.Register(hub.Subscriber{
		ID:       "builtin.honesty.imports",
		Events:   []hub.EventType{hub.EventToolPreUse},
		Mode:     hub.ModeGate, // fail-open: network may be unreliable
		Priority: 95,
		Handler:  ic.handle,
	})
}

func (ic *ImportChecker) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	if ev.Tool == nil {
		return &hub.HookResponse{Decision: hub.Allow}
	}
	name := ev.Tool.Name
	if name != "write" && name != "edit" && name != "str_replace_editor" {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	path, _ := ev.Tool.Input["path"].(string)
	if path == "" {
		path, _ = ev.Tool.Input["file_path"].(string)
	}

	content, _ := ev.Tool.Input["content"].(string)
	newString, _ := ev.Tool.Input["new_string"].(string)
	if content == "" {
		content = newString
	}
	if content == "" {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	// Extract imports based on file type
	var imports []ImportRef
	switch {
	case strings.HasSuffix(path, ".go"):
		imports = extractGoImports(content)
	case strings.HasSuffix(path, ".py"):
		imports = extractPyImports(content)
	case strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx") ||
		strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".jsx"):
		imports = extractJSImports(content)
	}

	if len(imports) == 0 {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	var hallucinated []string
	for _, imp := range imports {
		exists, err := ic.checkExists(ctx, imp)
		if err != nil {
			continue // fail-open
		}
		if !exists {
			hallucinated = append(hallucinated, imp.Name)
		}
	}

	if len(hallucinated) > 0 {
		return &hub.HookResponse{
			Decision: hub.Deny,
			Reason:   fmt.Sprintf("hallucinated package(s): %v — does not exist in registry", hallucinated),
		}
	}
	return &hub.HookResponse{Decision: hub.Allow}
}

// ImportRef is a reference to an external package.
type ImportRef struct {
	Name     string
	Language string // go, python, javascript
}

func (ic *ImportChecker) checkExists(ctx context.Context, imp ImportRef) (bool, error) {
	// Check cache
	if cached, ok := ic.cache.Load(imp.Name); ok {
		return cached.(bool), nil
	}

	var url string
	switch imp.Language {
	case "go":
		url = "https://proxy.golang.org/" + imp.Name + "/@v/list"
	case "python":
		url = "https://pypi.org/simple/" + imp.Name + "/"
	case "javascript":
		url = "https://registry.npmjs.org/" + imp.Name
	default:
		return true, nil // unknown language, allow
	}

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return true, err
	}

	resp, err := ic.httpClient.Do(req)
	if err != nil {
		return true, err // network failure, fail-open
	}
	resp.Body.Close()

	exists := resp.StatusCode == 200
	ic.cache.Store(imp.Name, exists)
	return exists, nil
}

func extractGoImports(content string) []ImportRef {
	var imports []ImportRef
	scanner := bufio.NewScanner(strings.NewReader(content))
	inImportBlock := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "import (" {
			inImportBlock = true
			continue
		}
		if inImportBlock && line == ")" {
			inImportBlock = false
			continue
		}
		if inImportBlock {
			// Extract import path from quoted string
			imp := extractQuotedImport(line)
			if imp != "" && isExternalGoImport(imp) {
				// Use the module root (first 3 path segments for github imports)
				imports = append(imports, ImportRef{Name: goModulePath(imp), Language: "go"})
			}
		}
		if strings.HasPrefix(line, "import \"") {
			imp := extractQuotedImport(line)
			if imp != "" && isExternalGoImport(imp) {
				imports = append(imports, ImportRef{Name: goModulePath(imp), Language: "go"})
			}
		}
	}
	return imports
}

func extractQuotedImport(line string) string {
	start := strings.Index(line, "\"")
	if start < 0 {
		return ""
	}
	end := strings.Index(line[start+1:], "\"")
	if end < 0 {
		return ""
	}
	return line[start+1 : start+1+end]
}

func isExternalGoImport(path string) bool {
	return strings.Contains(path, ".")
}

func goModulePath(importPath string) string {
	// For github.com/foo/bar/pkg, the module is github.com/foo/bar
	parts := strings.Split(importPath, "/")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], "/")
	}
	return importPath
}

func extractPyImports(content string) []ImportRef {
	var imports []ImportRef
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "import ") {
			pkg := strings.Fields(line)[1]
			pkg = strings.Split(pkg, ".")[0] // top-level package
			if !isPyStdlib(pkg) {
				imports = append(imports, ImportRef{Name: pkg, Language: "python"})
			}
		} else if strings.HasPrefix(line, "from ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				pkg := parts[1]
				pkg = strings.Split(pkg, ".")[0]
				if !isPyStdlib(pkg) {
					imports = append(imports, ImportRef{Name: pkg, Language: "python"})
				}
			}
		}
	}
	return imports
}

func isPyStdlib(pkg string) bool {
	stdlibs := map[string]bool{
		"os": true, "sys": true, "re": true, "json": true, "math": true,
		"time": true, "datetime": true, "collections": true, "functools": true,
		"itertools": true, "pathlib": true, "typing": true, "io": true,
		"abc": true, "copy": true, "enum": true, "dataclasses": true,
		"unittest": true, "pytest": true, "logging": true, "subprocess": true,
		"hashlib": true, "base64": true, "random": true, "string": true,
		"textwrap": true, "struct": true, "socket": true, "http": true,
		"urllib": true, "email": true, "html": true, "xml": true,
		"csv": true, "sqlite3": true, "threading": true, "multiprocessing": true,
		"asyncio": true, "contextlib": true, "inspect": true, "traceback": true,
		"warnings": true, "argparse": true, "shutil": true, "tempfile": true,
		"glob": true, "fnmatch": true, "stat": true, "gzip": true,
		"zipfile": true, "tarfile": true, "configparser": true, "secrets": true,
		"hmac": true, "signal": true, "pprint": true, "dis": true,
		"importlib": true, "pkgutil": true, "platform": true, "array": true,
		"bisect": true, "heapq": true, "queue": true, "decimal": true,
		"fractions": true, "operator": true, "weakref": true, "types": true,
	}
	return stdlibs[pkg]
}

func extractJSImports(content string) []ImportRef {
	var imports []ImportRef
	scanner := bufio.NewScanner(strings.NewReader(content))
	importRe := compileJSImportRe()
	requireRe := compileJSRequireRe()
	for scanner.Scan() {
		line := scanner.Text()
		if matches := importRe.FindStringSubmatch(line); len(matches) > 1 {
			pkg := matches[1]
			if !strings.HasPrefix(pkg, ".") && !strings.HasPrefix(pkg, "/") {
				// Get the package name (scoped or unscoped)
				imports = append(imports, ImportRef{Name: jsPackageName(pkg), Language: "javascript"})
			}
		}
		if matches := requireRe.FindStringSubmatch(line); len(matches) > 1 {
			pkg := matches[1]
			if !strings.HasPrefix(pkg, ".") && !strings.HasPrefix(pkg, "/") {
				imports = append(imports, ImportRef{Name: jsPackageName(pkg), Language: "javascript"})
			}
		}
	}
	return imports
}

func compileJSImportRe() *regexp.Regexp {
	return regexp.MustCompile(`(?:import\s+.*?from\s+|import\s+)['"]([^'"]+)['"]`)
}

func compileJSRequireRe() *regexp.Regexp {
	return regexp.MustCompile(`require\s*\(\s*['"]([^'"]+)['"]\s*\)`)
}

func jsPackageName(raw string) string {
	if strings.HasPrefix(raw, "@") {
		// Scoped package: @scope/name
		parts := strings.SplitN(raw, "/", 3)
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	// Unscoped: name or name/subpath
	parts := strings.SplitN(raw, "/", 2)
	return parts[0]
}
