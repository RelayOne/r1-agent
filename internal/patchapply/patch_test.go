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
	os.WriteFile(file, []byte(original), 0o600)

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
	os.WriteFile(file, []byte(original), 0o600)

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
	os.WriteFile(file, []byte(after), 0o600)

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
	os.WriteFile(file, []byte("package old"), 0o600)

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
	os.WriteFile(file, []byte("totally different\ncontent\nhere\n"), 0o600)

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

// TestApplyReverseUndoesCreateAndDelete covers audit A082: the package
// doc promises "Reverse application (undo a patch)", but reversing a
// patch that created or deleted files fell into the modify branch
// against "/dev/null" and silently failed.
func TestApplyReverseUndoesCreateAndDelete(t *testing.T) {
	root := t.TempDir()

	createPatch := `--- /dev/null
+++ b/created.txt
@@ -0,0 +1,2 @@
+hello
+world
`
	p, err := Parse(createPatch)
	if err != nil {
		t.Fatal(err)
	}
	if res := Apply(p, root); len(res.Failed) != 0 {
		t.Fatalf("forward apply failed: %v", res.Errors)
	}
	if res := ApplyReverse(p, root); len(res.Failed) != 0 {
		t.Fatalf("reverse of create failed: %v", res.Errors)
	}
	if _, err := os.Stat(filepath.Join(root, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("reverse of create did not remove the file (err=%v)", err)
	}

	// Delete patch: forward removes, reverse must recreate content.
	if err := os.WriteFile(filepath.Join(root, "doomed.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deletePatch := `--- a/doomed.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-a
-b
`
	p2, err := Parse(deletePatch)
	if err != nil {
		t.Fatal(err)
	}
	if res := Apply(p2, root); len(res.Failed) != 0 {
		t.Fatalf("forward delete failed: %v", res.Errors)
	}
	if res := ApplyReverse(p2, root); len(res.Failed) != 0 {
		t.Fatalf("reverse of delete failed: %v", res.Errors)
	}
	got, err := os.ReadFile(filepath.Join(root, "doomed.txt"))
	if err != nil || !strings.Contains(string(got), "a") || !strings.Contains(string(got), "b") {
		t.Fatalf("reverse of delete did not recreate content: %q err=%v", got, err)
	}
}

// TestParseNoNewlineMarkerModify covers the gap where git's
// "\ No newline at end of file" trailer (leading backslash) was stored as an
// OpContext content line, breaking context matching for a modify hunk. The
// marker must be dropped, and the real diff must still apply cleanly.
func TestParseNoNewlineMarkerModify(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "m.txt")
	// Original file has NO trailing newline.
	if err := os.WriteFile(file, []byte("alpha\nbeta"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A real `git diff` for changing the last (newline-less) line emits the
	// "\ No newline at end of file" trailers on both sides.
	diff := "--- a/m.txt\n" +
		"+++ b/m.txt\n" +
		"@@ -1,2 +1,2 @@\n" +
		" alpha\n" +
		"-beta\n" +
		"\\ No newline at end of file\n" +
		"+gamma\n" +
		"\\ No newline at end of file\n"

	p, err := Parse(diff)
	if err != nil {
		t.Fatal(err)
	}
	// The backslash marker must NOT have become a hunk line.
	for _, l := range p.Files[0].Hunks[0].Lines {
		if strings.HasPrefix(l.Text, "\\ No newline") {
			t.Fatalf("newline marker leaked into hunk line: %q", l.Text)
		}
	}

	res := Apply(p, dir)
	if len(res.Failed) != 0 {
		t.Fatalf("modify hunk with no-newline marker failed to apply: %v", res.Errors)
	}
	got, _ := os.ReadFile(file)
	if string(got) != "alpha\ngamma" {
		t.Fatalf("wrong result after applying: %q", string(got))
	}
}

// TestParseNoNewlineMarkerCreate covers the same gap for a create hunk: the
// marker must never be written verbatim into the newly created file.
func TestParseNoNewlineMarkerCreate(t *testing.T) {
	dir := t.TempDir()
	diff := "--- /dev/null\n" +
		"+++ b/n.txt\n" +
		"@@ -0,0 +1 @@\n" +
		"+only line\n" +
		"\\ No newline at end of file\n"

	p, err := Parse(diff)
	if err != nil {
		t.Fatal(err)
	}
	res := Apply(p, dir)
	if len(res.Failed) != 0 {
		t.Fatalf("create with no-newline marker failed: %v", res.Errors)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "n.txt"))
	if strings.Contains(string(got), "No newline") {
		t.Fatalf("marker written into created file: %q", string(got))
	}
	if string(got) != "only line" {
		t.Fatalf("created file has wrong content: %q", string(got))
	}
}

// TestApplyNewFileRefusesOverwrite covers the gap where an IsNew
// (/dev/null->file) hunk unconditionally overwrote an existing file,
// silently destroying its contents.
func TestApplyNewFileRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "keep.go")
	original := "package keep // precious\n"
	if err := os.WriteFile(existing, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	diff := "--- /dev/null\n" +
		"+++ b/keep.go\n" +
		"@@ -0,0 +1,1 @@\n" +
		"+package clobbered\n"
	p, err := Parse(diff)
	if err != nil {
		t.Fatal(err)
	}
	res := Apply(p, dir)
	if len(res.Failed) != 1 {
		t.Fatalf("create over existing file should fail, got failed=%v applied=%v", res.Failed, res.Applied)
	}
	// The original file must be untouched.
	got, _ := os.ReadFile(existing)
	if string(got) != original {
		t.Fatalf("existing file was clobbered by create hunk: %q", string(got))
	}
}

// TestApplyPreservesModeAtomically covers the gap where the modify write
// used non-atomic os.WriteFile with a hardcoded 0644, dropping the target's
// real mode. The rewrite must preserve the executable bit.
func TestApplyPreservesModeAtomically(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	original := "package main\n\nimport \"fmt\"\n\nfunc main() {\n}\n"
	if err := os.WriteFile(file, []byte(original), 0o755); err != nil {
		t.Fatal(err)
	}

	patch, _ := Parse(sampleDiff)
	res := Apply(patch, dir)
	if len(res.Failed) != 0 {
		t.Fatalf("apply failed: %v", res.Errors)
	}
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode not preserved across rewrite: got %o, want 0755", fi.Mode().Perm())
	}
}

// TestApplyWithAdditionsNoSpuriousErrors covers audit A023: the old
// hashline pre-check indexed hunk lines including '+' additions, which
// consume no old-file lines, so valid patches with additions produced
// spurious "concurrent edit detected" noise in result.Errors.
func TestApplyWithAdditionsNoSpuriousErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("l1\nl2\nl3\nl4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := `--- a/f.txt
+++ b/f.txt
@@ -1,4 +1,6 @@
 l1
+added-a
 l2
+added-b
 l3
 l4
`
	p, err := Parse(patch)
	if err != nil {
		t.Fatal(err)
	}
	res := Apply(p, root)
	if len(res.Failed) != 0 || len(res.Errors) != 0 {
		t.Fatalf("valid patch produced errors: failed=%v errors=%v", res.Failed, res.Errors)
	}
}
