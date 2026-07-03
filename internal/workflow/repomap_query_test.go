package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/repomap"
	"github.com/RelayOne/r1/internal/tfidf"
)

func TestTaskQueryScores(t *testing.T) {
	idx := tfidf.NewIndex()
	idx.AddDocument("widget.go", "widget rendering pipeline widget rendering draws frames")
	idx.AddDocument("db.go", "database connection pooling transactions")
	// Chunk-indexed documents carry a "#name" suffix that must be stripped.
	idx.AddDocument("chunked.go#RenderWidget", "widget rendering helper")
	idx.AddDocument("chunked.go#PoolConn", "widget cache")
	idx.Finalize()

	tests := []struct {
		name    string
		idx     *tfidf.Index
		task    string
		wantKey string
		skipKey string
		wantNil bool
	}{
		{name: "nil index", idx: nil, task: "widget", wantNil: true},
		{name: "blank task", idx: idx, task: "   ", wantNil: true},
		{name: "no match", idx: idx, task: "zzzz qqqq", wantNil: true},
		{name: "match keys by file", idx: idx, task: "fix widget rendering", wantKey: "widget.go", skipKey: "db.go"},
		{name: "chunk suffix stripped", idx: idx, task: "fix widget rendering", wantKey: "chunked.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taskQueryScores(tt.idx, tt.task)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("want nil scores, got %v", got)
				}
				return
			}
			if got[tt.wantKey] <= 0 {
				t.Errorf("missing positive score for %q: %v", tt.wantKey, got)
			}
			if tt.skipKey != "" {
				if _, ok := got[tt.skipKey]; ok {
					t.Errorf("unrelated file %q scored: %v", tt.skipKey, got)
				}
			}
			for k := range got {
				if strings.ContainsRune(k, '#') {
					t.Errorf("chunk suffix not stripped from key %q", k)
				}
			}
		})
	}
}

// TestExecutePromptQueryConditionedRepoMap proves the execute prompt's
// repomap section is conditioned on the task text: under a tight budget the
// TF-IDF-matched file wins the map slot from a statically higher-ranked
// decoy, and with TFIDF nil the static ranking still decides.
func TestExecutePromptQueryConditionedRepoMap(t *testing.T) {
	srcDir := t.TempDir()
	// Decoy: more exported symbols -> higher static rank. Vocabulary is
	// disjoint from the task text so TF-IDF cannot score it.
	decoy := `package decoy

// database connection pooling transactions ledger accounting
func ZzDecoyAlpha() {}
func ZzDecoyBeta() {}
func ZzDecoyGamma() {}
`
	// Target: fewer symbols, but its content matches the task text.
	target := `package widget

// widget rendering pipeline: widget rendering widget rendering frames
func ZzWidgetRender() {}
func ZzWidgetFrame() {}
`
	if err := os.WriteFile(filepath.Join(srcDir, "decoy.go"), []byte(decoy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "widget.go"), []byte(target), 0o600); err != nil {
		t.Fatal(err)
	}

	rm, err := repomap.Build(srcDir)
	if err != nil {
		t.Fatalf("repomap.Build: %v", err)
	}
	idx, err := tfidf.Build(srcDir, []string{".go"})
	if err != nil {
		t.Fatalf("tfidf.Build: %v", err)
	}

	// Budget fits the header plus exactly one file section (header 5,
	// decoy section 3+2*3=9, target section 3+2*2=7).
	base := Engine{
		Task:          "fix the widget rendering pipeline",
		RepoMap:       rm,
		RepoMapBudget: 15,
	}

	withoutTFIDF := executePromptWithContext(base)
	if !strings.Contains(withoutTFIDF, "ZzDecoyAlpha") {
		t.Errorf("static ranking should render the decoy without TFIDF; prompt:\n%s", withoutTFIDF)
	}
	if strings.Contains(withoutTFIDF, "ZzWidgetRender") {
		t.Errorf("budget should exclude the target without TFIDF; prompt:\n%s", withoutTFIDF)
	}

	withTFIDF := base
	withTFIDF.TFIDF = idx
	prompt := executePromptWithContext(withTFIDF)
	if !strings.Contains(prompt, "ZzWidgetRender") {
		t.Errorf("task-matched file missing from query-conditioned map; prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "ZzDecoyAlpha") {
		t.Errorf("decoy still won the map slot despite query boost; prompt:\n%s", prompt)
	}
}
