// artifact.go — artifact helpers for Bitbucket Pipelines (C5, T13).
//
// Bitbucket Pipelines auto-uploads files matching a step's `artifacts:` globs
// to its built-in artifact store (retained 14 days; max 1 GB per artifact).
// This file does NOT re-upload from Go; instead it exposes the canonical URL
// for a step's artifact archive and a download helper for runtime fetches.

package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ArtifactRefForStep returns the canonical URL for an artifact archive
// produced by one step inside a pipeline. The URL is suitable for embedding
// in PR comment summaries; an authenticated GET against it returns the
// archive bytes.
func ArtifactRefForStep(baseURL, workspace, repo, pipelineUUID, stepUUID string) string {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = strings.TrimRight(DefaultBaseURL, "/")
	}
	return fmt.Sprintf("%s/repositories/%s/%s/pipelines/%s/steps/%s/artifacts/",
		base,
		url.PathEscape(workspace), url.PathEscape(repo),
		url.PathEscape(pipelineUUID), url.PathEscape(stepUUID))
}

// DownloadArtifact fetches the named artifact (or the artifact archive when
// name is empty) for a step. Returns the raw bytes — caller decides how to
// unpack.
func (c *Client) DownloadArtifact(ctx context.Context, workspace, repo, pipelineUUID, stepUUID, name string) ([]byte, error) {
	if pipelineUUID == "" || stepUUID == "" {
		return nil, errors.New("bitbucket: DownloadArtifact: pipeline + step uuids required")
	}
	endpoint := fmt.Sprintf("/repositories/%s/%s/pipelines/%s/steps/%s/artifacts/",
		url.PathEscape(workspace), url.PathEscape(repo),
		url.PathEscape(pipelineUUID), url.PathEscape(stepUUID))
	if name != "" {
		endpoint += url.PathEscape(name)
	}
	body, err := c.doRaw(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("bitbucket: DownloadArtifact: %w", err)
	}
	return body, nil
}

// TracebundleGlobs lists the artifact glob patterns every R1 step declares.
// Kept as a sorted slice so the template renderer and the tests agree on
// exact wire-byte content.
func TracebundleGlobs() []string {
	return []string{
		"tracebundles/**/*.tar.zst",
		"audit-reports/**/*.json",
		"r1-review.json",
		"r1-mission.log",
	}
}
