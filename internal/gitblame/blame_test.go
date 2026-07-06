package gitblame

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func samplePorcelain() string {
	return "abc123def456abc123def456abc123def456abc1 1 1 3\n" +
		"author Alice\n" +
		"author-mail <alice@example.com>\n" +
		"author-time 1700000000\n" +
		"author-tz +0000\n" +
		"committer Alice\n" +
		"committer-mail <alice@example.com>\n" +
		"committer-time 1700000000\n" +
		"committer-tz +0000\n" +
		"summary initial commit\n" +
		"filename main.go\n" +
		"\tpackage main\n" +
		"abc123def456abc123def456abc123def456abc1 2 2\n" +
		"\timport \"fmt\"\n" +
		"def456abc123def456abc123def456abc123def4 3 3 2\n" +
		"author Bob\n" +
		"author-mail <bob@example.com>\n" +
		"author-time 1710000000\n" +
		"author-tz +0000\n" +
		"committer Bob\n" +
		"committer-mail <bob@example.com>\n" +
		"committer-time 1710000000\n" +
		"committer-tz +0000\n" +
		"summary add function\n" +
		"filename main.go\n" +
		"\tfunc main() {\n" +
		"def456abc123def456abc123def456abc123def4 4 4\n" +
		"\t}\n"
}

func TestParsePorcelain(t *testing.T) {
	fb, err := ParsePorcelain("main.go", samplePorcelain())
	if err != nil {
		t.Fatal(err)
	}

	if fb.Path != "main.go" {
		t.Errorf("expected main.go, got %s", fb.Path)
	}
	if len(fb.Lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(fb.Lines))
	}

	if fb.Lines[0].Author != "Alice" {
		t.Errorf("line 1 author: %s", fb.Lines[0].Author)
	}
	if fb.Lines[2].Author != "Bob" {
		t.Errorf("line 3 author: %s", fb.Lines[2].Author)
	}
}

func TestAuthors(t *testing.T) {
	fb, _ := ParsePorcelain("main.go", samplePorcelain())

	authors := fb.Authors()
	if len(authors) != 2 {
		t.Fatalf("expected 2 authors, got %d", len(authors))
	}

	// Alice has more lines
	if authors[0].Author != "Alice" || authors[0].Lines != 2 {
		t.Errorf("expected Alice with 2 lines, got %+v", authors[0])
	}
}

func TestLinesBy(t *testing.T) {
	fb, _ := ParsePorcelain("main.go", samplePorcelain())

	bobLines := fb.LinesBy("Bob")
	if len(bobLines) != 2 {
		t.Errorf("expected 2 Bob lines, got %d", len(bobLines))
	}
}

func TestLineRange(t *testing.T) {
	fb, _ := ParsePorcelain("main.go", samplePorcelain())

	lines := fb.LineRange(2, 3)
	if len(lines) != 2 {
		t.Errorf("expected 2 lines in range, got %d", len(lines))
	}
}

func TestAuthorsInRange(t *testing.T) {
	fb, _ := ParsePorcelain("main.go", samplePorcelain())

	authors := fb.AuthorsInRange(1, 4)
	if len(authors) != 2 {
		t.Errorf("expected 2 authors in full range, got %d", len(authors))
	}

	authors = fb.AuthorsInRange(3, 4)
	if len(authors) != 1 || authors[0] != "Bob" {
		t.Errorf("expected Bob only, got %v", authors)
	}
}

func TestClassifyFreshness(t *testing.T) {
	now := time.Now()

	if ClassifyFreshness(now.Add(-24*time.Hour)) != FreshRecent {
		t.Error("1 day ago should be recent")
	}
	if ClassifyFreshness(now.Add(-180*24*time.Hour)) != FreshModern {
		t.Error("180 days ago should be modern")
	}
	if ClassifyFreshness(now.Add(-2*365*24*time.Hour)) != FreshStale {
		t.Error("2 years ago should be stale")
	}
	if ClassifyFreshness(now.Add(-5*365*24*time.Hour)) != FreshAncient {
		t.Error("5 years ago should be ancient")
	}
}

func TestFreshnessDistribution(t *testing.T) {
	fb, _ := ParsePorcelain("main.go", samplePorcelain())

	dist := fb.FreshnessDistribution()
	if dist == nil {
		t.Fatal("distribution should not be nil")
	}

	total := 0.0
	for _, v := range dist {
		total += v
	}
	if total < 0.99 || total > 1.01 {
		t.Errorf("distribution should sum to 1.0, got %f", total)
	}
}

func TestImpactSummary(t *testing.T) {
	fb, _ := ParsePorcelain("main.go", samplePorcelain())

	summary := fb.ImpactSummary(1, 4)
	if summary == "" {
		t.Error("summary should not be empty")
	}
	if summary == "no lines in range" {
		t.Error("should have lines in range")
	}
}

func TestImpactSummaryEmpty(t *testing.T) {
	fb := &FileBlame{Path: "empty.go"}
	summary := fb.ImpactSummary(1, 10)
	if summary != "no lines in range" {
		t.Errorf("expected no lines, got %s", summary)
	}
}

// TestParsePorcelainSHA256 proves the header parser accepts SHA-256 (64-hex)
// object names, not just 40-hex SHA-1, so blame works in sha256 repos.
func TestParsePorcelainSHA256(t *testing.T) {
	sha := "0000000000000000000000000000000000000000000000000000000000000abc" // 64 hex
	out := sha + " 1 1 1\n" +
		"author Carol\n" +
		"author-time 1700000000\n" +
		"\tpackage main\n"
	fb, err := ParsePorcelain("main.go", out)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.Lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(fb.Lines))
	}
	if fb.Lines[0].Commit != sha {
		t.Errorf("expected 64-hex commit %s, got %q", sha, fb.Lines[0].Commit)
	}
	if fb.Lines[0].Author != "Carol" {
		t.Errorf("expected Carol, got %q", fb.Lines[0].Author)
	}
}

// TestParsePorcelainMalformed proves a stray content line with no preceding
// header is not emitted as a line with an empty commit/author, and a bad
// author-time does not corrupt parsing.
func TestParsePorcelainMalformed(t *testing.T) {
	out := "\torphan content line before any header\n" +
		"abc123def456abc123def456abc123def456abc1 1 1 1\n" +
		"author Dave\n" +
		"author-time not-a-number\n" +
		"\treal line\n"
	fb, err := ParsePorcelain("x.go", out)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.Lines) != 1 {
		t.Fatalf("expected only the well-formed line, got %d: %+v", len(fb.Lines), fb.Lines)
	}
	if fb.Lines[0].Author != "Dave" || fb.Lines[0].Content != "real line" {
		t.Errorf("unexpected line: %+v", fb.Lines[0])
	}
	if !fb.Lines[0].Date.IsZero() {
		t.Errorf("bad author-time should leave a zero date, got %v", fb.Lines[0].Date)
	}
}

// TestBlameDashPrefixedPath proves the "--" separator makes Blame treat a
// path that looks like a flag (leading "-") as a pathspec. Without "--", git
// parses "-weird.txt" as an unknown option and the blame fails.
func TestBlameDashPrefixedPath(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitPath, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
			"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	const name = "-weird.txt" // a path that looks like a flag
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, name), []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// "--" ends option parsing for add/commit as well.
	run("add", "--", name)
	run("commit", "-q", "-m", "add dash-prefixed file")

	fb, err := Blame(dir, name)
	if err != nil {
		t.Fatalf("Blame on dash-prefixed path failed (missing '--' separator?): %v", err)
	}
	if len(fb.Lines) != 2 {
		t.Fatalf("expected 2 blamed lines, got %d", len(fb.Lines))
	}
	if fb.Lines[0].Author != "Tester" {
		t.Errorf("expected author Tester, got %q", fb.Lines[0].Author)
	}
}

func TestEmptyBlame(t *testing.T) {
	fb, err := ParsePorcelain("empty.go", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.Lines) != 0 {
		t.Error("empty blame should have no lines")
	}
	if fb.FreshnessDistribution() != nil {
		t.Error("empty file should have nil distribution")
	}
}
