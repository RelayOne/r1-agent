package mission

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBranchReaperReapMergedDeletesOldSameRepoBranches(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	var deleted []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/pulls":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"merged_at":"2026-04-20T12:00:00Z","head":{"ref":"feature/old","repo":{"full_name":"org/repo"}},"base":{"ref":"main"}},
				{"merged_at":"2026-04-28T12:00:00Z","head":{"ref":"feature/recent","repo":{"full_name":"org/repo"}},"base":{"ref":"main"}},
				{"merged_at":"2026-04-18T12:00:00Z","head":{"ref":"fork/branch","repo":{"full_name":"someone/fork"}},"base":{"ref":"main"}},
				{"merged_at":"2026-04-15T12:00:00Z","head":{"ref":"main","repo":{"full_name":"org/repo"}},"base":{"ref":"main"}}
			]`))
		case r.Method == http.MethodDelete && r.URL.EscapedPath() == "/repos/org/repo/git/refs/heads/feature%2Fold":
			deleted = append(deleted, "feature/old")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	reaper := BranchReaper{
		Repo:    "org/repo",
		Days:    7,
		baseURL: srv.URL,
		client:  srv.Client(),
		now: func() time.Time {
			return now
		},
	}

	got, err := reaper.ReapMerged(context.Background())
	if err != nil {
		t.Fatalf("ReapMerged: %v", err)
	}
	if len(got) != 1 || got[0] != "feature/old" {
		t.Fatalf("deleted branches = %v, want [feature/old]", got)
	}
	if len(deleted) != 1 || deleted[0] != "feature/old" {
		t.Fatalf("delete calls = %v, want [feature/old]", deleted)
	}
}

func TestBranchReaperReapMergedReturnsDeleteError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/pulls":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"merged_at":"2026-04-20T12:00:00Z","head":{"ref":"feature/old","repo":{"full_name":"org/repo"}},"base":{"ref":"main"}}
			]`))
		case r.Method == http.MethodDelete && r.URL.EscapedPath() == "/repos/org/repo/git/refs/heads/feature%2Fold":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	reaper := BranchReaper{
		Repo:    "org/repo",
		Days:    7,
		baseURL: srv.URL,
		client:  srv.Client(),
		now: func() time.Time {
			return time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
		},
	}

	if _, err := reaper.ReapMerged(context.Background()); err == nil {
		t.Fatal("expected delete error")
	}
}
