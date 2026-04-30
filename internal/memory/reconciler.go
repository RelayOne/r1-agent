package memory

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MemoryKind classifies the kind of repo artifact a memory references.
type MemoryKind string

const (
	// MemoryKindFile validates repo-relative file paths with os.Stat.
	MemoryKindFile MemoryKind = "file"
	// MemoryKindBranch validates GitHub branches via gh api.
	MemoryKindBranch MemoryKind = "branch"
	// MemoryKindService validates Cloud Run services via gcloud run services list.
	MemoryKindService MemoryKind = "service"
)

// Memory is a memory item that names a concrete artifact that may drift.
type Memory struct {
	ID     string
	Kind   MemoryKind
	Target string
	Note   string
}

// Drift describes a memory whose referenced artifact no longer exists.
type Drift struct {
	Memory     Memory
	Kind       MemoryKind
	Target     string
	Reason     string
	Suggestion string
}

// Reconciler validates a bounded sample of artifact-backed memories for drift.
type Reconciler struct {
	Memories []Memory
}

const reconcilerSampleSize = 5

var (
	reconcilerFileExists = func(path string) (bool, error) {
		_, err := os.Stat(path)
		if err == nil {
			return true, nil
		}
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	reconcilerBranchExists = func(ctx context.Context, branch string) (bool, error) {
		repo, err := reconcilerOriginRepo(ctx)
		if err != nil {
			return false, err
		}
		out, err := exec.CommandContext(ctx, "gh", "api",
			fmt.Sprintf("repos/%s/branches/%s", repo, url.PathEscape(branch))).CombinedOutput()
		if err == nil {
			return true, nil
		}
		if isNotFoundOutput(out) {
			return false, nil
		}
		return false, fmt.Errorf("gh api branch lookup %q: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	reconcilerServiceExists = func(ctx context.Context, service string) (bool, error) {
		out, err := exec.CommandContext(ctx, "gcloud", "run", "services", "list",
			"--format=value(metadata.name)").CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("gcloud run services list: %w: %s", err, strings.TrimSpace(string(out)))
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(line) == service {
				return true, nil
			}
		}
		return false, nil
	}
	reconcilerRepoRoot = func() (string, error) {
		return os.Getwd()
	}
)

// Reconcile validates up to a bounded sample of memories and returns drift findings.
func (r Reconciler) Reconcile(ctx context.Context) ([]Drift, error) {
	limit := len(r.Memories)
	if limit > reconcilerSampleSize {
		limit = reconcilerSampleSize
	}

	root, err := reconcilerRepoRoot()
	if err != nil {
		return nil, fmt.Errorf("resolve repo root: %w", err)
	}

	drifts := make([]Drift, 0, limit)
	for _, memory := range r.Memories[:limit] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		drift, ok, err := reconcileMemory(ctx, root, memory)
		if err != nil {
			return nil, err
		}
		if ok {
			drifts = append(drifts, drift)
		}
	}
	return drifts, nil
}

func reconcileMemory(ctx context.Context, root string, memory Memory) (Drift, bool, error) {
	switch normalizeMemoryKind(memory.Kind) {
	case MemoryKindFile:
		target := filepath.Join(root, filepath.Clean(memory.Target))
		exists, err := reconcilerFileExists(target)
		if err != nil {
			return Drift{}, false, fmt.Errorf("check file memory %q: %w", memory.Target, err)
		}
		if exists {
			return Drift{}, false, nil
		}
		return Drift{
			Memory:     memory,
			Kind:       MemoryKindFile,
			Target:     memory.Target,
			Reason:     "referenced file no longer exists",
			Suggestion: "update the file path or remove the memory",
		}, true, nil
	case MemoryKindBranch:
		exists, err := reconcilerBranchExists(ctx, memory.Target)
		if err != nil {
			return Drift{}, false, fmt.Errorf("check branch memory %q: %w", memory.Target, err)
		}
		if exists {
			return Drift{}, false, nil
		}
		return Drift{
			Memory:     memory,
			Kind:       MemoryKindBranch,
			Target:     memory.Target,
			Reason:     "referenced branch no longer exists",
			Suggestion: "update the branch name or remove the memory",
		}, true, nil
	case MemoryKindService:
		exists, err := reconcilerServiceExists(ctx, memory.Target)
		if err != nil {
			return Drift{}, false, fmt.Errorf("check service memory %q: %w", memory.Target, err)
		}
		if exists {
			return Drift{}, false, nil
		}
		return Drift{
			Memory:     memory,
			Kind:       MemoryKindService,
			Target:     memory.Target,
			Reason:     "referenced service no longer exists",
			Suggestion: "update the service name or remove the memory",
		}, true, nil
	default:
		return Drift{}, false, nil
	}
}

func normalizeMemoryKind(kind MemoryKind) MemoryKind {
	switch MemoryKind(strings.ToLower(strings.TrimSpace(string(kind)))) {
	case MemoryKindFile:
		return MemoryKindFile
	case MemoryKindBranch:
		return MemoryKindBranch
	case MemoryKindService:
		return MemoryKindService
	default:
		return ""
	}
}

func reconcilerOriginRepo(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git config --get remote.origin.url: %w: %s", err, strings.TrimSpace(string(out)))
	}
	repo, err := parseGitHubRemote(strings.TrimSpace(string(out)))
	if err != nil {
		return "", err
	}
	return repo, nil
}

func parseGitHubRemote(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	remote = strings.TrimSuffix(remote, ".git")

	switch {
	case strings.HasPrefix(remote, "git@github.com:"):
		return strings.TrimPrefix(remote, "git@github.com:"), nil
	case strings.HasPrefix(remote, "https://github.com/"):
		return strings.TrimPrefix(remote, "https://github.com/"), nil
	case strings.HasPrefix(remote, "ssh://git@github.com/"):
		return strings.TrimPrefix(remote, "ssh://git@github.com/"), nil
	default:
		return "", fmt.Errorf("unsupported GitHub remote %q", remote)
	}
}

func isNotFoundOutput(out []byte) bool {
	trimmed := strings.ToLower(strings.TrimSpace(string(bytes.TrimSpace(out))))
	return strings.Contains(trimmed, "404") || strings.Contains(trimmed, "not found")
}
