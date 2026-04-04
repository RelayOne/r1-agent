package patchapply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var sampleDiff = "--- a/main.go\n" +
	"+++ b/main.go\n" +
	"@@ -1,5 +1,6 @@\n" +
	" package main\n" +
	"\n" +
	"-import \"fmt\"\n" +
	"+import (\n" +
	"+\t\"fmt\"\n" +
	"+)\n" +
	"\n" +
	" func main() {\n"

func TestParseDiff(t *testing.T) {
	patch, err := Parse(sampleDiff)
	if err != nil {
		t.Fatal(err)
	}

	if len(patch.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(patch.Files))
	}

	fp := patch.Files[0]
	if fp.OldPath != "main.go" {
		t.Errorf("old path: %s", fp.OldPath)
	}
	if fp.NewPath != "main.go" {
		t.Errorf("new path: %s", fp.NewPath)
	}
	if len(fp.Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(fp.Hunks))
	}

	h := fp.Hunks[0]
	if h.OldStart != 1 || h.OldCount != 5 {
		t.Errorf("old range: %d,%d", h.OldStart, h.OldCount)
	}
	if h.NewStart != 1 || h.NewCount != 6 {
		t.Errorf("new range: %d,%d", h.NewStart, h.NewCount)
	}
}

func TestParseStats(t *testing.T) {
	patch, _ := Parse(sampleDiff)
	files, adds, dels := patch.Stats()
	if files != 1 {
		t.Errorf("expected 1 file, got %d", files)
	}
	if adds != 3 {
		t.Errorf("expected 3 additions, got %d", adds)
	}
	if dels != 1 {
		t.Errorf("expected 1 deletion, got %d", dels)
	}
}

func TestApplyPatch(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	original := "package main\n\nimport \"fmt\"\n\nfunc main() {\n}\n"
	os.WriteFile(file, []byte(original), 0644)

	patch, _ := Parse(sampleDiff)
	result := Apply(patch, dir)

	if len(result.Applied) != 1 {
		t.Errorf("expected 1 applied, got %d", len(result.Applied))
	}
	if len(result.Failed) != 0 {
		t.Errorf("unexpected failures: %v", result.Errors)
	}

	content, _ := os.ReadFile(file)
	if !strings.Contains(string(content), "import (") {
		t.Error("patch should have been applied")
	}
}

func TestDryRun(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	original := "package main\n\nimport \"fmt\"\n\nfunc main() {\n}\n"
	os.WriteFile(file, []byte(original), 0644)

	patch, _ := Parse(sampleDiff)
	result := DryRun(patch, dir)

	if len(result.Applied) != 1 {
		t.Errorf("dry run should report applied: %v", result.Errors)
	}

	// File should be unchanged
	content, _ := os.ReadFile(file)
	if strings.Contains(string(content), "import (") {
		t.Error("dry run should not modify file")
	}
}

func TestApplyReverse(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	// Start with the "after" state
	after := "package main\n\nimport (\n\t\"fmt\"\n)\n\nfunc main() {\n}\n"
	os.WriteFile(file, []byte(after), 0644)

	patch, _ := Parse(sampleDiff)
	result := ApplyReverse(patch, dir)

	if len(result.Applied) != 1 {
		t.Errorf("reverse apply failed: %v", result.Errors)
	}

	content, _ := os.ReadFile(file)
	if !strings.Contains(string(content), "import \"fmt\"") {
		t.Error("reverse should restore original import")
	}
}

func TestApplyNewFile(t *testing.T) {
	dir := t.TempDir()

	diff := "--- /dev/null\n" +
		"+++ b/new.go\n" +
		"@@ -0,0 +1,3 @@\n" +
		"+package new\n" +
		"+\n" +
		"+func Hello() {}\n"
	patch, err := Parse(diff)
	if err != nil {
		t.Fatal(err)
	}

	if !patch.Files[0].IsNew {
		t.Error("should detect new file")
	}

	result := Apply(patch, dir)
	if len(result.Applied) != 1 {
		t.Errorf("new file should be applied: %v", result.Errors)
	}

	content, err := os.ReadFile(filepath.Join(dir, "new.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "package new") {
		t.Error("new file should have content")
	}
}

func TestApplyDeleteFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "old.go")
	os.WriteFile(file, []byte("package old"), 0644)

	diff := "--- a/old.go\n" +
		"+++ /dev/null\n" +
		"@@ -1 +0,0 @@\n" +
		"-package old\n"
	patch, _ := Parse(diff)
	if !patch.Files[0].IsDelete {
		t.Error("should detect deleted file")
	}

	result := Apply(patch, dir)
	if len(result.Applied) != 1 {
		t.Errorf("delete should succeed: %v", result.Errors)
	}

	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("file should be deleted")
	}
}

func TestApplyContextMismatch(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	// Different content than what patch expects
	os.WriteFile(file, []byte("totally different\ncontent\nhere\n"), 0644)

	patch, _ := Parse(sampleDiff)
	result := Apply(patch, dir)

	if len(result.Failed) != 1 {
		t.Error("should fail on context mismatch")
	}
}

func TestParseSummary(t *testing.T) {
	patch, _ := Parse(sampleDiff)
	s := patch.Summary()
	if s == "" {
		t.Error("summary should not be empty")
	}
	if !strings.Contains(s, "main.go") {
		t.Error("summary should mention file")
	}
}

func TestParseMultipleFiles(t *testing.T) {
	diff := "--- a/a.go\n" +
		"+++ b/a.go\n" +
		"@@ -1,3 +1,3 @@\n" +
		" package a\n" +
		"\n" +
		"-var x = 1\n" +
		"+var x = 2\n" +
		"--- a/b.go\n" +
		"+++ b/b.go\n" +
		"@@ -1,3 +1,3 @@\n" +
		" package b\n" +
		"\n" +
		"-var y = 1\n" +
		"+var y = 2\n"
	patch, err := Parse(diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch.Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(patch.Files))
	}
}

func TestParseHunkHeader(t *testing.T) {
	tests := []struct {
		line                string
		oldStart, oldCount  int
		newStart, newCount  int
	}{
		{"@@ -1,5 +1,6 @@", 1, 5, 1, 6},
		{"@@ -10,3 +12,7 @@ func foo()", 10, 3, 12, 7},
		{"@@ -1 +1 @@", 1, 1, 1, 1},
	}

	for _, tc := range tests {
		hunk, err := parseHunkHeader(tc.line)
		if err != nil {
			t.Errorf("parse %q: %v", tc.line, err)
			continue
		}
		if hunk.OldStart != tc.oldStart || hunk.OldCount != tc.oldCount {
			t.Errorf("%q: old=%d,%d want %d,%d", tc.line, hunk.OldStart, hunk.OldCount, tc.oldStart, tc.oldCount)
		}
		if hunk.NewStart != tc.newStart || hunk.NewCount != tc.newCount {
			t.Errorf("%q: new=%d,%d want %d,%d", tc.line, hunk.NewStart, hunk.NewCount, tc.newStart, tc.newCount)
		}
	}
}

func TestStripPrefix(t *testing.T) {
	if stripPrefix("a/main.go") != "main.go" {
		t.Error("should strip a/")
	}
	if stripPrefix("b/main.go") != "main.go" {
		t.Error("should strip b/")
	}
	if stripPrefix("main.go") != "main.go" {
		t.Error("should not strip without prefix")
	}
}
