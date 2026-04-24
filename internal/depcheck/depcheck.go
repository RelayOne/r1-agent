// Package depcheck validates that every npm dependency declared in a
// package.json exists in the registry before a session is allowed to
// claim success.
//
// This catches the dependency-hallucination failure mode directly: an
// LLM worker confidently writes `"@nativewind/style": "^0.0.1"` into
// package.json; no reviewer bothers to resolve it; `pnpm install` then
// fails with ERR_PNPM_FETCH_404; every downstream AC fails with
// `tsc: not found` / `turbo: not found`; the reasoning loop misattributes
// the symptom as an AC-command bug and spins. One registry HEAD per
// declared dep would have caught it in seconds.
//
// Scope for v1:
//   - npm only (package.json). PyPI/crates.io extensions are additive.
//   - Deps are resolved against `https://registry.npmjs.org/<name>` via
//     HEAD. Non-200 means the name does not exist (or the registry is
//     down — we treat that as "unknown" and do not flag).
//   - Workspace refs (`workspace:*`), local file: refs, git:/github:
//     refs, and relative paths are skipped — they don't hit the public
//     registry.
//
// Cached for the lifetime of the process so repeated runs against the
// same SOW don't re-pay registry round-trips on deps that are
// definitely fine.
package depcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// packageJSONFile is the canonical npm/Node manifest filename. Centralised
// so the walker, manifest classifier, and resolver agree byte-for-byte.
const packageJSONFile = "package.json"

// Finding is one hallucinated or otherwise unresolvable dependency.
type Finding struct {
	// PackageJSON is the absolute path of the package.json that
	// declared the offending dep.
	PackageJSON string
	// Name is the dep name as declared in package.json (scoped or not).
	Name string
	// Version is the version string as declared. Not used in lookups;
	// included in the finding for context.
	Version string
	// Section is "dependencies" / "devDependencies" / "peerDependencies"
	// / "optionalDependencies".
	Section string
	// Reason is a short human-readable description of why the dep was
	// flagged (e.g., "not found in npm registry").
	Reason string
}

// Client is a depcheck configuration. Safe for concurrent use once
// constructed. Zero value is usable; it talks to the public npm
// registry with a 10-second per-request timeout.
type Client struct {
	// Registry is the base URL of the npm registry. Defaults to
	// "https://registry.npmjs.org" when empty.
	Registry string
	// PyPI / Crates / GoMod override the corresponding public
	// registries. Empty values fall back to DefaultRegistries().
	PyPI   string
	Crates string
	GoMod  string
	// HTTP is the http.Client used for registry lookups. Defaults to
	// a 10-second-timeout client when nil.
	HTTP *http.Client

	cacheMu sync.Mutex
	cache   map[string]cacheEntry
}

type cacheEntry struct {
	exists bool
	// When lookupErr is non-nil, we saw a transport-level failure
	// and didn't learn anything definitive. Don't flag.
	lookupErr error
}

// DefaultClient returns a *Client wired to the public npm registry.
func DefaultClient() *Client {
	return &Client{
		Registry: "https://registry.npmjs.org",
		HTTP: &http.Client{
			Timeout: 10 * time.Second,
		},
		cache: map[string]cacheEntry{},
	}
}

// ValidatePackageJSON reads path, parses its dep sections, and returns
// a Finding for every dep that is definitely absent from the registry.
// Workspace / file / git / github refs are skipped. Refs we can't
// resolve due to transport errors are silently skipped (so a dead
// registry doesn't block a build).
//
// Returns an error only if path can't be read / parsed. A non-existent
// package is reported as a Finding, not an error.
func (c *Client) ValidatePackageJSON(ctx context.Context, path string) ([]Finding, error) {
	if c.cache == nil {
		c.cache = map[string]cacheEntry{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("depcheck: read %s: %w", path, err)
	}
	var pkg struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("depcheck: parse %s: %w", path, err)
	}

	var findings []Finding
	check := func(section string, deps map[string]string) {
		for name, version := range deps {
			if skipDep(version) {
				continue
			}
			exists, lookupErr := c.exists(ctx, name)
			if lookupErr != nil {
				// Transport error; can't conclude anything. A dead
				// registry must not block a real build.
				continue
			}
			if !exists {
				findings = append(findings, Finding{
					PackageJSON: path,
					Name:        name,
					Version:     version,
					Section:     section,
					Reason:      "not found in npm registry (hallucinated dependency name?)",
				})
			}
		}
	}
	check("dependencies", pkg.Dependencies)
	check("devDependencies", pkg.DevDependencies)
	check("peerDependencies", pkg.PeerDependencies)
	check("optionalDependencies", pkg.OptionalDependencies)
	return findings, nil
}

// ValidateTree validates every package.json under root (skipping
// node_modules directories). Useful as a foundation-sanity pre-check.
func (c *Client) ValidateTree(ctx context.Context, root string) ([]Finding, error) {
	var paths []string
	err := walkPackageJSONs(root, func(p string) { paths = append(paths, p) })
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, p := range paths {
		fs, err := c.ValidatePackageJSON(ctx, p)
		if err != nil {
			// Malformed package.json — keep going, surface later.
			continue
		}
		out = append(out, fs...)
	}
	return out, nil
}

// exists issues a HEAD against <Registry>/<name> and caches the result.
// Returns (ok, nil) for a definitive answer, (_, err) for a transport
// failure we cannot interpret.
func (c *Client) exists(ctx context.Context, name string) (bool, error) {
	c.cacheMu.Lock()
	if e, ok := c.cache[name]; ok {
		c.cacheMu.Unlock()
		return e.exists, e.lookupErr
	}
	c.cacheMu.Unlock()

	registry := c.Registry
	if registry == "" {
		registry = "https://registry.npmjs.org"
	}
	httpc := c.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 10 * time.Second}
	}

	// Scoped names (@scope/pkg) must be URL-escaped as a single path
	// segment; unscoped names are passed through.
	u := registry + "/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return false, err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		c.cacheSet(name, cacheEntry{lookupErr: err})
		return false, err
	}
	defer resp.Body.Close()

	var exists bool
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		exists = true
	case resp.StatusCode == 404:
		exists = false
	default:
		// 5xx, 429, other transient — treat as unknown. Don't flag.
		e := cacheEntry{lookupErr: fmt.Errorf("registry returned %d", resp.StatusCode)}
		c.cacheSet(name, e)
		return false, e.lookupErr
	}
	c.cacheSet(name, cacheEntry{exists: exists})
	return exists, nil
}

func (c *Client) cacheSet(name string, entry cacheEntry) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.cache == nil {
		c.cache = map[string]cacheEntry{}
	}
	c.cache[name] = entry
}

// skipDep returns true when the dep's version specifier points somewhere
// other than the public npm registry. These cannot be validated with a
// registry HEAD and are skipped to avoid false positives.
func skipDep(version string) bool {
	v := strings.TrimSpace(version)
	if v == "" {
		return true
	}
	// pnpm / yarn workspace refs
	if strings.HasPrefix(v, "workspace:") {
		return true
	}
	// local file path (file:../foo) and link:
	if strings.HasPrefix(v, "file:") || strings.HasPrefix(v, "link:") || strings.HasPrefix(v, "portal:") {
		return true
	}
	// git / github: / bitbucket: / gitlab: / http(s) tarball
	if strings.HasPrefix(v, "git") || strings.HasPrefix(v, "github:") || strings.HasPrefix(v, "bitbucket:") || strings.HasPrefix(v, "gitlab:") {
		return true
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return true
	}
	// npm: alias (npm:real-name@x.y.z) — resolvable but different shape;
	// v1 does not resolve aliases to avoid false positives.
	if strings.HasPrefix(v, "npm:") {
		return true
	}
	// Relative path or plain file
	if strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../") || strings.HasPrefix(v, "/") {
		return true
	}
	return false
}

// walkPackageJSONs calls visit for every package.json under root, skipping
// node_modules and dot-directories. Kept small and allocation-light.
func walkPackageJSONs(root string, visit func(string)) error {
	return osWalkFunc(root, func(path string, isDir bool) error {
		base := pathBase(path)
		if isDir {
			if base == "node_modules" || (strings.HasPrefix(base, ".") && base != "." && base != root) {
				return errSkipDir
			}
			return nil
		}
		if base == packageJSONFile {
			visit(path)
		}
		return nil
	})
}
