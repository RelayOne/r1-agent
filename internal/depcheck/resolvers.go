// Language-agnostic dependency resolution. Each ecosystem has its own
// registry shape, its own manifest format, and its own set of "skip
// this because it isn't really a registry ref" conventions.
//
// The top-level Validate(ctx, root) entry walks the repo, identifies
// each manifest it recognizes, and dispatches to the matching resolver.
// Findings from every resolver are merged into one list so a caller
// sees a single language-agnostic report.
//
// Resolvers talk to public registries via HEAD (or, where HEAD is not
// supported, a cheap GET). Transport errors are silently dropped so a
// dead registry never blocks a real build.

package depcheck

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Registries bundles per-ecosystem endpoints. Zero value is the public
// set; tests override individual URLs with httptest servers.
type Registries struct {
	NPM    string // default: https://registry.npmjs.org
	PyPI   string // default: https://pypi.org/pypi
	Crates string // default: https://crates.io/api/v1/crates
	GoMod  string // default: https://proxy.golang.org
}

// DefaultRegistries returns the public endpoints.
func DefaultRegistries() Registries {
	return Registries{
		NPM:    "https://registry.npmjs.org",
		PyPI:   "https://pypi.org/pypi",
		Crates: "https://crates.io/api/v1/crates",
		GoMod:  "https://proxy.golang.org",
	}
}

// Validate walks root (skipping node_modules, target/, __pycache__,
// .git and dot-directories) and validates every recognized manifest.
// Recognized manifests and their resolvers:
//
//   - package.json          → npm (registry.npmjs.org)
//   - requirements.txt      → PyPI (pypi.org)
//   - pyproject.toml        → PyPI (project.dependencies + [project] deps)
//   - Cargo.toml            → crates.io
//   - go.mod                → Go module proxy (proxy.golang.org)
//
// Unknown file types are skipped without error. The returned list is
// empty when everything resolves or when the network is unreachable.
func (c *Client) Validate(ctx context.Context, root string) ([]Finding, error) {
	var manifests []string
	err := osWalkFunc(root, func(path string, isDir bool) error {
		base := filepath.Base(path)
		if isDir {
			if shouldSkipDir(base) {
				return errSkipDir
			}
			return nil
		}
		switch base {
		case "package.json", "requirements.txt", "pyproject.toml", "Cargo.toml", "go.mod":
			manifests = append(manifests, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var out []Finding
	for _, m := range manifests {
		fs, err := c.validateManifest(ctx, m)
		if err != nil {
			// Malformed manifest — skip; foundation-sanity will
			// notice separately via the real build/install step.
			continue
		}
		out = append(out, fs...)
	}
	return out, nil
}

func shouldSkipDir(name string) bool {
	switch name {
	case "node_modules", "target", "__pycache__", ".git", "vendor", "dist", "build", ".next":
		return true
	}
	return strings.HasPrefix(name, ".") && name != "."
}

func (c *Client) validateManifest(ctx context.Context, path string) ([]Finding, error) {
	base := filepath.Base(path)
	switch base {
	case "package.json":
		return c.ValidatePackageJSON(ctx, path)
	case "requirements.txt":
		return c.validatePythonRequirements(ctx, path)
	case "pyproject.toml":
		return c.validatePyprojectTOML(ctx, path)
	case "Cargo.toml":
		return c.validateCargoTOML(ctx, path)
	case "go.mod":
		return c.validateGoMod(ctx, path)
	}
	return nil, nil
}

// -------- PyPI (requirements.txt + pyproject.toml [project.dependencies])

var pythonReqLine = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)(?:\s*\[[^\]]*\])?`)

func (c *Client) validatePythonRequirements(ctx context.Context, path string) ([]Finding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var findings []Finding
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			// -r, -e, -c directives are out of scope; skip to avoid
			// following includes we can't safely validate.
			continue
		}
		// Tolerate markers ("; python_version>='3.8'"), inline URLs
		// (git+, https://...), path refs (./local). Skip any line that
		// isn't a bare or version-specified package name.
		if strings.Contains(line, "://") || strings.HasPrefix(line, "./") || strings.HasPrefix(line, "/") {
			continue
		}
		m := pythonReqLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		exists, err := c.pypiExists(ctx, name)
		if err != nil {
			continue
		}
		if !exists {
			findings = append(findings, Finding{
				PackageJSON: path,
				Name:        name,
				Section:     "requirements.txt",
				Reason:      "not found on PyPI (hallucinated package name?)",
			})
		}
	}
	return findings, sc.Err()
}

// validatePyprojectTOML does a minimal regex-driven parse of
// [project.dependencies] and [project.optional-dependencies.*] arrays.
// We avoid adding a full TOML parser dependency for v1; the false-negative
// rate on exotic pyproject shapes is acceptable (pypi.org HEAD is free
// and foundation-sanity catches missing packages at install time).
func (c *Client) validatePyprojectTOML(ctx context.Context, path string) ([]Finding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	txt := string(data)
	// Match [project] dependencies and optional-dependencies arrays.
	// Both shapes look like:
	//   dependencies = ["numpy>=1.24", "pandas"]
	depArray := regexp.MustCompile(`(?s)dependencies\s*=\s*\[(.*?)\]`)
	nameInItem := regexp.MustCompile(`^\s*"([A-Za-z0-9][A-Za-z0-9._-]*)`)
	var findings []Finding
	for _, arr := range depArray.FindAllStringSubmatch(txt, -1) {
		items := strings.Split(arr[1], ",")
		for _, it := range items {
			m := nameInItem.FindStringSubmatch(it)
			if m == nil {
				continue
			}
			name := m[1]
			exists, err := c.pypiExists(ctx, name)
			if err != nil {
				continue
			}
			if !exists {
				findings = append(findings, Finding{
					PackageJSON: path,
					Name:        name,
					Section:     "pyproject.toml",
					Reason:      "not found on PyPI",
				})
			}
		}
	}
	return findings, nil
}

func (c *Client) pypiExists(ctx context.Context, name string) (bool, error) {
	registries := c.registries()
	base := registries.PyPI
	u := base + "/" + name + "/json"
	return c.simpleHeadOrGet(ctx, "pypi:"+name, u)
}

// -------- crates.io

func (c *Client) validateCargoTOML(ctx context.Context, path string) ([]Finding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	txt := string(data)

	// [dependencies], [dev-dependencies], [build-dependencies] sections.
	// A section entry is either:
	//   name = "version"
	//   name = { version = "x.y.z", ... }
	section := regexp.MustCompile(`(?m)^\s*\[(dependencies|dev-dependencies|build-dependencies)\]\s*$`)
	entryLine := regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_-]+)\s*=\s*(.+)$`)
	locs := section.FindAllStringIndex(txt, -1)
	if len(locs) == 0 {
		return nil, nil
	}
	locs = append(locs, []int{len(txt), len(txt)})
	var findings []Finding
	for i := 0; i < len(locs)-1; i++ {
		block := txt[locs[i][1]:locs[i+1][0]]
		sectionName := section.FindString(txt[locs[i][0]:locs[i][1]])
		sectionName = strings.Trim(sectionName, "[]\n ")
		for _, m := range entryLine.FindAllStringSubmatch(block, -1) {
			if strings.HasPrefix(strings.TrimSpace(m[0]), "[") {
				continue
			}
			name := m[1]
			rhs := strings.TrimSpace(m[2])
			if skipCargoRHS(rhs) {
				continue
			}
			exists, err := c.cratesExists(ctx, name)
			if err != nil {
				continue
			}
			if !exists {
				findings = append(findings, Finding{
					PackageJSON: path,
					Name:        name,
					Section:     sectionName,
					Reason:      "not found on crates.io",
				})
			}
		}
	}
	return findings, nil
}

func skipCargoRHS(rhs string) bool {
	// git = "...", path = "./...", workspace = true → skip
	low := strings.ToLower(rhs)
	if strings.Contains(low, "git ") || strings.Contains(low, "git=") || strings.Contains(low, `git = `) || strings.Contains(low, "git:") {
		return true
	}
	if strings.Contains(low, "path ") || strings.Contains(low, "path=") || strings.Contains(low, `path = `) {
		return true
	}
	if strings.Contains(low, "workspace ") || strings.Contains(low, "workspace=") || strings.Contains(low, `workspace = true`) {
		return true
	}
	return false
}

func (c *Client) cratesExists(ctx context.Context, name string) (bool, error) {
	registries := c.registries()
	u := registries.Crates + "/" + name
	return c.simpleHeadOrGet(ctx, "crates:"+name, u)
}

// -------- Go modules

func (c *Client) validateGoMod(ctx context.Context, path string) ([]Finding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var findings []Finding
	sc := bufio.NewScanner(f)
	inRequire := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "require (") {
			inRequire = true
			continue
		}
		if inRequire && line == ")" {
			inRequire = false
			continue
		}
		var mod string
		if inRequire {
			mod = firstField(line)
		} else if strings.HasPrefix(line, "require ") {
			mod = firstField(strings.TrimPrefix(line, "require "))
		}
		if mod == "" {
			continue
		}
		if !strings.Contains(mod, ".") {
			// std-lib-ish or malformed; skip.
			continue
		}
		exists, err := c.gomodExists(ctx, mod)
		if err != nil {
			continue
		}
		if !exists {
			findings = append(findings, Finding{
				PackageJSON: path,
				Name:        mod,
				Section:     "go.mod",
				Reason:      "module not found on the Go module proxy",
			})
		}
	}
	return findings, sc.Err()
}

func (c *Client) gomodExists(ctx context.Context, module string) (bool, error) {
	registries := c.registries()
	// proxy.golang.org serves .../@latest for every known module.
	u := registries.GoMod + "/" + module + "/@latest"
	return c.simpleHeadOrGet(ctx, "go:"+module, u)
}

// -------- shared helpers

func firstField(s string) string {
	for i, r := range s {
		if r == ' ' || r == '\t' {
			return s[:i]
		}
	}
	return s
}

func (c *Client) registries() Registries {
	r := DefaultRegistries()
	if c.Registry != "" {
		r.NPM = c.Registry
	}
	if c.PyPI != "" {
		r.PyPI = c.PyPI
	}
	if c.Crates != "" {
		r.Crates = c.Crates
	}
	if c.GoMod != "" {
		r.GoMod = c.GoMod
	}
	return r
}

// simpleHeadOrGet is a cached existence check. key must uniquely
// identify the (ecosystem, name) tuple so two ecosystems with the same
// package name do not share cache entries.
func (c *Client) simpleHeadOrGet(ctx context.Context, key, url string) (bool, error) {
	c.cacheMu.Lock()
	if e, ok := c.cache[key]; ok {
		c.cacheMu.Unlock()
		return e.exists, e.lookupErr
	}
	c.cacheMu.Unlock()

	httpc := c.HTTP
	if httpc == nil {
		httpc = DefaultClient().HTTP
	}
	// Try HEAD first; some registries (PyPI, crates.io) don't support
	// HEAD on every path, so fall back to GET on 405.
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		c.cacheSet(key, cacheEntry{lookupErr: err})
		return false, err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusMethodNotAllowed {
		req2, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err = httpc.Do(req2)
		if err != nil {
			c.cacheSet(key, cacheEntry{lookupErr: err})
			return false, err
		}
		resp.Body.Close()
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		c.cacheSet(key, cacheEntry{exists: true})
		return true, nil
	case resp.StatusCode == 404 || resp.StatusCode == 410:
		c.cacheSet(key, cacheEntry{exists: false})
		return false, nil
	default:
		e := cacheEntry{lookupErr: fmt.Errorf("registry returned %d", resp.StatusCode)}
		c.cacheSet(key, e)
		return false, e.lookupErr
	}
}

