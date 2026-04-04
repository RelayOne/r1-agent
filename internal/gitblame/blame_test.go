package gitblame

import (
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
