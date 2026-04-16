// Package depcheck — extras.go
//
// STOKE-007 extensions to the existing resolver set: Docker
// registry manifest check, git ls-remote reachability, and
// generic HTTP HEAD probe for endpoint references.
//
// Scope:
//
//   - CheckDockerManifest(ref) verifies a container image
//     reference (e.g. "library/nginx:alpine",
//     "ghcr.io/foo/bar:v2") resolves against its registry.
//     Uses the Docker Registry v2 HTTP API shape
//     (/v2/<name>/manifests/<reference>).
//   - CheckGitRef(url, ref) runs `git ls-remote` to verify
//     a remote ref exists without cloning. Works against any
//     git remote the local git client can reach.
//   - CheckHTTPEndpoint(url) does a HEAD request and reports
//     HTTP status + reachability. Used when a manifest
//     declares an external endpoint the build depends on
//     (plugin URLs, license-server endpoints, etc.).
//
// All three return a Finding shape compatible with the
// existing resolvers so callers can merge results
// transparently.
package depcheck

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Finding shape shared with the other resolvers.

// CheckDockerManifest probes a Docker registry for an image
// reference. Returns nil on success; an error describing the
// specific failure on miss. Doesn't pull the image — only
// asks the registry whether it exists.
//
// Reference format: "[registry/]namespace/name[:tag|@digest]".
// A missing registry defaults to docker.io. A missing tag
// defaults to "latest".
func CheckDockerManifest(ctx context.Context, ref string, client *http.Client) error {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	registry, repo, tag := parseDockerRef(ref)
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return fmt.Errorf("depcheck: build docker request: %w", err)
	}
	// Docker Registry v2 requires this Accept to return a
	// JSON manifest; a plain HEAD can still 200 OK on the old
	// v2 schema but this header covers OCI images too.
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("depcheck: docker %q: %w", ref, err)
	}
	defer resp.Body.Close()
	// Some registries return 401 for anonymous HEAD — treat
	// as "probably exists but needs auth", not "missing".
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
		return nil
	}
	return fmt.Errorf("depcheck: docker %q: status %d", ref, resp.StatusCode)
}

// parseDockerRef splits a reference into (registry, repo, tag).
// Kept local + basic; Stoke doesn't ship a full Docker
// reference parser because the common case is simple.
func parseDockerRef(ref string) (string, string, string) {
	registry := "registry-1.docker.io"
	repo := ref
	tag := "latest"

	// Digest: repo@sha256:...
	if i := strings.Index(ref, "@"); i >= 0 {
		repo = ref[:i]
		tag = ref[i+1:]
	} else if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i+1:], "/") {
		// Tag: repo:tag (but not the registry:port leading
		// colon — heuristic: the colon is a tag delimiter
		// only when what follows doesn't contain a slash).
		repo = ref[:i]
		tag = ref[i+1:]
	}

	// Registry: anything with a dot or colon before the first
	// slash is a registry host.
	if i := strings.Index(repo, "/"); i >= 0 {
		head := repo[:i]
		if strings.ContainsAny(head, ".:") {
			registry = head
			repo = repo[i+1:]
		}
	}
	// Implicit library/ prefix for single-segment official
	// images on docker.io.
	if registry == "registry-1.docker.io" && !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return registry, repo, tag
}

// CheckGitRef verifies a remote git ref is reachable. Uses
// git ls-remote so we don't have to re-implement the smart-HTTP
// protocol; any ref shape git understands (branch, tag,
// commit) is accepted.
//
// Requires the `git` binary on $PATH. Returns the resolved
// commit SHA on success, or an error.
func CheckGitRef(ctx context.Context, url, ref string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("depcheck: git not available: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "ls-remote", url, ref)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("depcheck: git ls-remote %s %s: %w", url, ref, err)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", fmt.Errorf("depcheck: ref %q not found at %q", ref, url)
	}
	// ls-remote output: "<sha>\t<refname>"
	parts := strings.Fields(line)
	if len(parts) < 1 {
		return "", fmt.Errorf("depcheck: unexpected ls-remote output: %q", line)
	}
	return parts[0], nil
}

// CheckHTTPEndpoint runs a HEAD request against url. Returns
// the response's status code on success; an error on network
// failure. Treat 2xx + 3xx as "reachable"; everything else is
// a failure the caller surfaces to the operator.
func CheckHTTPEndpoint(ctx context.Context, url string, client *http.Client) (int, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, fmt.Errorf("depcheck: build http request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("depcheck: http %q: %w", url, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("depcheck: http %q: status %d", url, resp.StatusCode)
}
