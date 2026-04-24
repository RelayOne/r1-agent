package hashline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeTag(t *testing.T) {
	tag1 := ComputeTag("func main() {")
	tag2 := ComputeTag("func main() {")
	tag3 := ComputeTag("func Main() {")

	if tag1 != tag2 {
		t.Error("identical lines should have same tag")
	}
	if tag1 == tag3 {
		t.Error("different lines should have different tags")
	}
	if len(tag1) != 2 {
		t.Errorf("expected 2-char tag, got %d: %q", len(tag1), tag1)
	}
}

func TestTagFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"), 0o600)

	tf, err := TagFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tf.Lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(tf.Lines))
	}
	if tf.Lines[0].Content != "package main" {
		t.Errorf("unexpected first line: %q", tf.Lines[0].Content)
	}
	if tf.Lines[0].Tag == "" {
		t.Error("tag should not be empty")
	}
}

func TestRender(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("line one\nline two\nline three\n"), 0o600)

	tf, _ := TagFile(path)
	rendered := tf.Render()

	if !strings.Contains(rendered, "1#") {
		t.Error("expected line 1 with tag")
	}
	if !strings.Contains(rendered, "line one") {
		t.Error("expected content")
	}

	// Range render
	ranged := tf.RenderRange(2, 3)
	if strings.Contains(ranged, "line one") {
		t.Error("line one should not be in range 2-3")
	}
	if !strings.Contains(ranged, "line two") {
		t.Error("expected line two in range")
	}
}

func TestVerifySuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o600)

	tf, _ := TagFile(path)
	v := NewVerifier()

	result := v.Verify(EditRequest{
		Path:         path,
		StartLine:    2,
		EndLine:      2,
		ExpectedTags: []Tag{tf.Lines[1].Tag},
		NewContent:   []string{"BETA"},
	})

	if !result.Applied {
		t.Errorf("edit should succeed: %v", result.Conflicts)
	}

	// Verify file was modified
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "BETA") {
		t.Error("expected BETA in file")
	}
	if !strings.Contains(string(data), "alpha") {
		t.Error("expected alpha preserved")
	}
	if !strings.Contains(string(data), "gamma") {
		t.Error("expected gamma preserved")
	}
}

func TestVerifyConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o600)

	// Agent reads file
	tf, _ := TagFile(path)
	oldTag := tf.Lines[1].Tag

	// Another agent modifies the file
	os.WriteFile(path, []byte("alpha\nmodified\ngamma\n"), 0o600)

	v := NewVerifier()
	result := v.Verify(EditRequest{
		Path:         path,
		StartLine:    2,
		EndLine:      2,
		ExpectedTags: []Tag{oldTag},
		NewContent:   []string{"BETA"},
	})

	if result.Applied {
		t.Error("edit should fail due to conflict")
	}
	if len(result.Conflicts) == 0 {
		t.Error("expected conflict descriptions")
	}
	if !strings.Contains(result.Conflicts[0], "content changed") {
		t.Errorf("expected 'content changed' in conflict: %s", result.Conflicts[0])
	}
}

func TestVerifyMultiLineEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0o600)

	tf, _ := TagFile(path)
	v := NewVerifier()

	result := v.Verify(EditRequest{
		Path:         path,
		StartLine:    2,
		EndLine:      4,
		ExpectedTags: []Tag{tf.Lines[1].Tag, tf.Lines[2].Tag, tf.Lines[3].Tag},
		NewContent:   []string{"X", "Y"},
	})

	if !result.Applied {
		t.Errorf("multi-line edit should succeed: %v", result.Conflicts)
	}

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 4 { // a, X, Y, e
		t.Errorf("expected 4 lines, got %d: %v", len(lines), lines)
	}
}

func TestVerifyInvalidRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\n"), 0o600)

	v := NewVerifier()
	result := v.Verify(EditRequest{
		Path:      path,
		StartLine: 1,
		EndLine:   5,
		ExpectedTags: []Tag{"xx"},
		NewContent: []string{"X"},
	})
	if result.Applied {
		t.Error("should fail for out-of-range")
	}
}

func TestGetTag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello\nworld\n"), 0o600)

	tf, _ := TagFile(path)
	tag, ok := tf.GetTag(1)
	if !ok || tag == "" {
		t.Error("expected valid tag for line 1")
	}
	_, ok = tf.GetTag(0)
	if ok {
		t.Error("expected false for line 0")
	}
	_, ok = tf.GetTag(100)
	if ok {
		t.Error("expected false for out-of-range")
	}
}
