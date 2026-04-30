package mission

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// BranchReaper deletes merged branches that have lingered past a retention window.
type BranchReaper struct {
	Repo string
	Days int

	client  *http.Client
	baseURL string
	token   string
	now     func() time.Time
}

type branchReaperPull struct {
	MergedAt *time.Time `json:"merged_at"`
	Head     struct {
		Ref  string `json:"ref"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// ReapMerged deletes merged branches in the configured repo that are older than the retention window.
func (r BranchReaper) ReapMerged(ctx context.Context) ([]string, error) {
	if strings.TrimSpace(r.Repo) == "" {
		return nil, fmt.Errorf("mission.BranchReaper: repo is required")
	}
	pulls, err := r.listClosedPulls(ctx)
	if err != nil {
		return nil, err
	}

	cutoff := r.clock().AddDate(0, 0, -r.retentionDays())
	var deleted []string
	for _, pr := range pulls {
		if !r.shouldDelete(pr, cutoff) {
			continue
		}
		if err := r.deleteBranch(ctx, pr.Head.Ref); err != nil {
			return deleted, err
		}
		deleted = append(deleted, pr.Head.Ref)
	}
	return deleted, nil
}

func (r BranchReaper) listClosedPulls(ctx context.Context) ([]branchReaperPull, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/pulls?state=closed&per_page=100&page=1", r.apiBaseURL(), r.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	r.addHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("mission.BranchReaper: list merged branches: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mission.BranchReaper: list merged branches: status %d", resp.StatusCode)
	}

	var pulls []branchReaperPull
	if err := json.NewDecoder(resp.Body).Decode(&pulls); err != nil {
		return nil, fmt.Errorf("mission.BranchReaper: decode pull list: %w", err)
	}
	return pulls, nil
}

func (r BranchReaper) shouldDelete(pr branchReaperPull, cutoff time.Time) bool {
	if pr.MergedAt == nil || pr.MergedAt.After(cutoff) {
		return false
	}
	if strings.TrimSpace(pr.Head.Ref) == "" || pr.Head.Ref == pr.Base.Ref {
		return false
	}
	if pr.Head.Repo == nil || pr.Head.Repo.FullName != r.Repo {
		return false
	}
	return true
}

func (r BranchReaper) deleteBranch(ctx context.Context, branch string) error {
	endpoint := fmt.Sprintf("%s/repos/%s/git/refs/heads/%s", r.apiBaseURL(), r.Repo, url.PathEscape(branch))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	r.addHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("mission.BranchReaper: delete branch %q: %w", branch, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("mission.BranchReaper: delete branch %q: status %d", branch, resp.StatusCode)
	}
	return nil
}

func (r BranchReaper) retentionDays() int {
	if r.Days > 0 {
		return r.Days
	}
	return 7
}

func (r BranchReaper) apiBaseURL() string {
	if strings.TrimSpace(r.baseURL) != "" {
		return strings.TrimRight(r.baseURL, "/")
	}
	return "https://api.github.com"
}

func (r BranchReaper) httpClient() *http.Client {
	if r.client != nil {
		return r.client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (r BranchReaper) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now().UTC()
}

func (r BranchReaper) authToken() string {
	if strings.TrimSpace(r.token) != "" {
		return r.token
	}
	if token := strings.TrimSpace(os.Getenv("GH_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
}

func (r BranchReaper) addHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := r.authToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}
