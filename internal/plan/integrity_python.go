// Package plan — integrity_python.go
//
// Python ecosystem. Validates `import X` / `from X import Y` against
// pyproject.toml / requirements.txt / setup.py / Pipfile. Public
// surface: when a package uses an explicit __all__ in __init__.py,
// a new sibling module should be added there. Compile regression:
// prefers pyright, falls back to mypy, then py_compile.
package plan

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func init() {
	RegisterEcosystem(&pythonEcosystem{})
}

type pythonEcosystem struct{}

func (pythonEcosystem) Name() string { return "python" }

func (pythonEcosystem) Owns(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".py"
}

var pyImportRE = regexp.MustCompile(`(?m)^\s*(?:from\s+([A-Za-z_][A-Za-z0-9_\.]*)\s+import|import\s+([A-Za-z_][A-Za-z0-9_\.]*))`)

func (pythonEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	manifest, deps := pyFindManifest(projectRoot)
	if manifest == "" {
		return nil, nil
	}
	stdlib := pyStdlibSet()
	localPkgs := pyLocalPackages(projectRoot)
	var out []ManifestMiss
	seen := map[string]struct{}{}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range pyImportRE.FindAllStringSubmatch(string(body), -1) {
			spec := m[1]
			if spec == "" {
				spec = m[2]
			}
			root := spec
			if i := strings.Index(root, "."); i > 0 {
				root = root[:i]
			}
			if root == "" {
				continue
			}
			if _, ok := stdlib[root]; ok {
				continue
			}
			if _, ok := localPkgs[root]; ok {
				continue
			}
			// Relative import (from . import x) starts with empty
			// spec; handled by the regex capturing a name, but the
			// "." prefix check below keeps them out.
			if strings.HasPrefix(spec, ".") {
				continue
			}
			normalized := strings.ReplaceAll(root, "_", "-")
			if _, ok := deps[root]; ok {
				continue
			}
			if _, ok := deps[normalized]; ok {
				continue
			}
			key := f + "::" + root
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			relFile, _ := filepath.Rel(projectRoot, f)
			relMani, _ := filepath.Rel(projectRoot, manifest)
			out = append(out, ManifestMiss{
				SourceFile: relFile,
				ImportPath: root,
				Manifest:   relMani,
				AddCommand: fmt.Sprintf("pip install %s  # then record in %s", normalized, relMani),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SourceFile != out[j].SourceFile {
			return out[i].SourceFile < out[j].SourceFile
		}
		return out[i].ImportPath < out[j].ImportPath
	})
	return out, nil
}

// Public surface: when a package's __init__.py declares an explicit
// __all__ list, a new sibling module should be added to it. If there
// is no __all__, Python's default is "everything importable" — no
// action required.
func (pythonEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	out := make([]PublicSurfaceMiss, 0, len(files))
	for _, f := range files {
		base := filepath.Base(f)
		if base == "__init__.py" || strings.HasPrefix(base, "_") || strings.HasSuffix(base, "_test.py") {
			continue
		}
		dir := filepath.Dir(f)
		initFile := filepath.Join(dir, "__init__.py")
		info, err := os.Stat(initFile)
		if err != nil || info.IsDir() {
			continue
		}
		initBody, err := os.ReadFile(initFile)
		if err != nil {
			continue
		}
		if !strings.Contains(string(initBody), "__all__") {
			continue // implicit-export package; nothing to do
		}
		modName := strings.TrimSuffix(base, ".py")
		if strings.Contains(string(initBody), `"`+modName+`"`) || strings.Contains(string(initBody), `'`+modName+`'`) {
			continue
		}
		srcRel, _ := filepath.Rel(projectRoot, f)
		tgtRel, _ := filepath.Rel(projectRoot, initFile)
		out = append(out, PublicSurfaceMiss{
			SourceFile: srcRel,
			TargetFile: tgtRel,
			FixLine:    fmt.Sprintf(`__all__.append("%s")  # or add %q to the existing __all__ list`, modName, modName),
		})
	}
	return out, nil
}

var pyrightErrRE = regexp.MustCompile(`^(.+?):(\d+):(\d+)\s+-\s+error:\s+(.*)$`)
var mypyErrRE = regexp.MustCompile(`^(.+?):(\d+):\s*(?:(\d+):)?\s*error:\s*(.*)$`)

func (pythonEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	if len(files) == 0 {
		return nil, nil
	}
	c, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	// Prefer pyright, then mypy, then py_compile.
	if _, err := exec.LookPath("pyright"); err == nil {
		args := append([]string{"--outputjson=false"}, files...)
		cmd := exec.CommandContext(c, "pyright", args...) // #nosec G204 -- language toolchain binary invoked with Stoke-generated args.
		cmd.Dir = projectRoot
		out, _ := cmd.CombinedOutput()
		return pyParseErrors(projectRoot, string(out), pyrightErrRE, "pyright"), nil
	}
	if _, err := exec.LookPath("mypy"); err == nil {
		args := append([]string{"--show-column-numbers", "--no-color-output", "--no-error-summary"}, files...)
		cmd := exec.CommandContext(c, "mypy", args...) // #nosec G204 -- language toolchain binary invoked with Stoke-generated args.
		cmd.Dir = projectRoot
		out, _ := cmd.CombinedOutput()
		return pyParseErrors(projectRoot, string(out), mypyErrRE, "mypy"), nil
	}
	if _, err := exec.LookPath("python3"); err == nil {
		// Final fallback: py_compile catches syntax errors only.
		var errs []CompileErr
		for _, f := range files {
			cmd := exec.CommandContext(c, "python3", "-m", "py_compile", f) // #nosec G204 -- language toolchain binary invoked with Stoke-generated args.
			cmd.Dir = projectRoot
			out, _ := cmd.CombinedOutput()
			if len(out) > 0 {
				rel, _ := filepath.Rel(projectRoot, f)
				errs = append(errs, CompileErr{
					File: rel, Line: 0, Code: "py_compile",
					Message: strings.TrimSpace(string(out)),
				})
			}
		}
		return errs, nil
	}
	return nil, nil
}

func pyParseErrors(projectRoot, output string, re *regexp.Regexp, code string) []CompileErr {
	lines := strings.Split(output, "\n")
	errs := make([]CompileErr, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		file := m[1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(projectRoot, file)
		}
		rel, _ := filepath.Rel(projectRoot, file)
		var lno, col int
		fmt.Sscanf(m[2], "%d", &lno)
		// mypy's column may be in group 3, pyright's in group 3 too.
		if len(m) >= 5 && m[3] != "" {
			fmt.Sscanf(m[3], "%d", &col)
		}
		msg := m[len(m)-1]
		errs = append(errs, CompileErr{File: rel, Line: lno, Column: col, Code: code, Message: msg})
	}
	return errs
}

// pyFindManifest looks for the preferred-in-order manifest files
// and returns the first one found plus its declared dependency set.
func pyFindManifest(projectRoot string) (string, map[string]struct{}) {
	candidates := []string{"pyproject.toml", "requirements.txt", "setup.py", "Pipfile"}
	for _, c := range candidates {
		p := filepath.Join(projectRoot, c)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, pyReadManifestDeps(p)
		}
	}
	return "", nil
}

func pyReadManifestDeps(path string) map[string]struct{} {
	out := map[string]struct{}{}
	body, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	base := filepath.Base(path)
	text := string(body)
	switch base {
	case "requirements.txt":
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
				continue
			}
			name := pySplitRequirement(line)
			if name != "" {
				out[name] = struct{}{}
				out[strings.ReplaceAll(name, "-", "_")] = struct{}{}
			}
		}
	case "pyproject.toml":
		// Scan [project.dependencies], [tool.poetry.dependencies],
		// and [project.optional-dependencies].
		scanner := bufio.NewScanner(strings.NewReader(text))
		section := ""
		depRE := regexp.MustCompile(`^([A-Za-z0-9_.\-]+)\s*=`)
		listRE := regexp.MustCompile(`^\s*"([A-Za-z0-9_.\-]+)[^"]*"`)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "[") {
				section = strings.Trim(line, "[]")
				continue
			}
			if strings.Contains(section, "dependencies") {
				if m := depRE.FindStringSubmatch(line); m != nil {
					out[m[1]] = struct{}{}
					out[strings.ReplaceAll(m[1], "-", "_")] = struct{}{}
				}
				if m := listRE.FindStringSubmatch(line); m != nil {
					out[m[1]] = struct{}{}
					out[strings.ReplaceAll(m[1], "-", "_")] = struct{}{}
				}
			}
		}
	case "setup.py":
		// Best-effort: scan for install_requires=[...] literals.
		re := regexp.MustCompile(`"([A-Za-z0-9_.\-]+)[^"]*"`)
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			out[m[1]] = struct{}{}
			out[strings.ReplaceAll(m[1], "-", "_")] = struct{}{}
		}
	case "Pipfile":
		re := regexp.MustCompile(`(?m)^([A-Za-z0-9_.\-]+)\s*=`)
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			out[m[1]] = struct{}{}
			out[strings.ReplaceAll(m[1], "-", "_")] = struct{}{}
		}
	}
	return out
}

func pySplitRequirement(line string) string {
	// Accept `pkg==1.2`, `pkg>=1.2`, `pkg[extra]`, `pkg ; marker`.
	for _, sep := range []string{"==", ">=", "<=", "!=", "~=", ">", "<", ";", " "} {
		if i := strings.Index(line, sep); i > 0 {
			line = line[:i]
		}
	}
	line = strings.TrimSpace(line)
	if i := strings.Index(line, "["); i > 0 {
		line = line[:i]
	}
	return line
}

// pyLocalPackages scans projectRoot for top-level directories that
// contain an __init__.py and treats those package names as resolvable.
func pyLocalPackages(projectRoot string) map[string]struct{} {
	out := map[string]struct{}{}
	entries, err := os.ReadDir(projectRoot)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		init := filepath.Join(projectRoot, e.Name(), "__init__.py")
		if info, err := os.Stat(init); err == nil && !info.IsDir() {
			out[e.Name()] = struct{}{}
		}
	}
	// src-layout fallback.
	srcDir := filepath.Join(projectRoot, "src")
	if info, err := os.Stat(srcDir); err == nil && info.IsDir() {
		srcEntries, _ := os.ReadDir(srcDir)
		for _, e := range srcEntries {
			if !e.IsDir() {
				continue
			}
			init := filepath.Join(srcDir, e.Name(), "__init__.py")
			if info, err := os.Stat(init); err == nil && !info.IsDir() {
				out[e.Name()] = struct{}{}
			}
		}
	}
	return out
}

// pyStdlibSet is a conservative set of the most common Python
// standard-library top-level modules. We only need "recognize
// stdlib so we don't flag it"; overbroad coverage here is fine.
func pyStdlibSet() map[string]struct{} {
	names := []string{
		"abc", "argparse", "ast", "asyncio", "base64", "binascii",
		"bisect", "builtins", "bz2", "calendar", "collections", "colorsys",
		"concurrent", "configparser", "contextlib", "contextvars", "copy",
		"csv", "ctypes", "dataclasses", "datetime", "decimal", "difflib",
		"dis", "doctest", "email", "encodings", "enum", "errno", "faulthandler",
		"fcntl", "filecmp", "fileinput", "fnmatch", "fractions", "ftplib",
		"functools", "gc", "getopt", "getpass", "gettext", "glob", "graphlib",
		"gzip", "hashlib", "heapq", "hmac", "html", "http", "imaplib", "importlib",
		"inspect", "io", "ipaddress", "itertools", "json", "keyword",
		"linecache", "locale", "logging", "lzma", "mailbox", "math",
		"mimetypes", "mmap", "multiprocessing", "netrc", "numbers",
		"operator", "os", "pathlib", "pickle", "pkgutil", "platform",
		"plistlib", "poplib", "posixpath", "pprint", "profile", "pstats",
		"pty", "pwd", "py_compile", "queue", "quopri", "random", "re",
		"readline", "reprlib", "resource", "runpy", "sched", "secrets",
		"select", "selectors", "shelve", "shlex", "shutil", "signal",
		"site", "smtplib", "socket", "socketserver", "sqlite3", "ssl",
		"stat", "statistics", "string", "stringprep", "struct", "subprocess",
		"sunau", "symtable", "sys", "sysconfig", "syslog", "tabnanny",
		"tarfile", "telnetlib", "tempfile", "termios", "textwrap", "threading",
		"time", "timeit", "tkinter", "token", "tokenize", "tomllib", "trace",
		"traceback", "tracemalloc", "tty", "turtle", "types", "typing",
		"unicodedata", "unittest", "urllib", "uu", "uuid", "venv", "warnings",
		"wave", "weakref", "webbrowser", "winreg", "winsound", "wsgiref",
		"xml", "xmlrpc", "zipapp", "zipfile", "zipimport", "zlib", "zoneinfo",
		"__future__", "__main__",
	}
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}
