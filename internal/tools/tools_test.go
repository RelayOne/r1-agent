package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefinitions(t *testing.T) {
	r := NewRegistry("/tmp")
	defs := r.Definitions()
	if len(defs) != 9 {
		t.Errorf("expected 9 tool definitions, got %d", len(defs))
	}
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, name := range []string{"read_file", "edit_file", "write_file", "bash", "grep", "glob", "env_exec", "env_copy_in", "env_copy_out"} {
		if !names[name] {
			t.Errorf("missing tool definition: %s", name)
		}
	}
}

func TestHandleRead(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("line 1\nline 2\nline 3\n"), 0644)

	r := NewRegistry(dir)
	result, err := r.Handle(context.Background(), "read_file", toJSON(map[string]string{"path": "test.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "1\tline 1") {
		t.Error("should contain line 1 with number")
	}
	if !strings.Contains(result, "3\tline 3") {
		t.Error("should contain line 3")
	}
}

func TestHandleReadOffset(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, "line "+string(rune('0'+i)))
	}
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Join(lines, "\n")), 0644)

	r := NewRegistry(dir)
	result, err := r.Handle(context.Background(), "read_file",
		toJSON(map[string]interface{}{"path": "big.txt", "offset": 3, "limit": 2}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "3\t") {
		t.Error("should start at line 3")
	}
}

func TestHandleReadMissing(t *testing.T) {
	r := NewRegistry(t.TempDir())
	_, err := r.Handle(context.Background(), "read_file", toJSON(map[string]string{"path": "missing.txt"}))
	if err == nil {
		t.Error("should error on missing file")
	}
}

func TestHandleEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("hello world\ngoodbye world\n"), 0644)

	r := NewRegistry(dir)
	// Must read first
	_, err := r.Handle(context.Background(), "edit_file",
		toJSON(map[string]interface{}{"path": "test.go", "old_string": "hello", "new_string": "hi"}))
	if err == nil {
		t.Error("should require read before edit")
	}

	// Read, then edit
	r.Handle(context.Background(), "read_file", toJSON(map[string]string{"path": "test.go"}))
	result, err := r.Handle(context.Background(), "edit_file",
		toJSON(map[string]interface{}{"path": "test.go", "old_string": "hello", "new_string": "hi"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "successfully") {
		t.Error("should report success")
	}

	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "hi world") {
		t.Error("file should contain 'hi world'")
	}
}

func TestHandleEditUniqueness(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dup.go")
	os.WriteFile(path, []byte("foo bar\nfoo baz\n"), 0644)

	r := NewRegistry(dir)
	r.Handle(context.Background(), "read_file", toJSON(map[string]string{"path": "dup.go"}))

	// "foo" appears twice — should fail
	_, err := r.Handle(context.Background(), "edit_file",
		toJSON(map[string]interface{}{"path": "dup.go", "old_string": "foo", "new_string": "qux"}))
	if err == nil {
		t.Error("should fail when old_string matches multiple times")
	}
	if !strings.Contains(err.Error(), "2 times") {
		t.Errorf("error should mention count: %v", err)
	}
}

func TestHandleEditReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.go")
	os.WriteFile(path, []byte("foo bar\nfoo baz\n"), 0644)

	r := NewRegistry(dir)
	r.Handle(context.Background(), "read_file", toJSON(map[string]string{"path": "multi.go"}))

	result, err := r.Handle(context.Background(), "edit_file",
		toJSON(map[string]interface{}{"path": "multi.go", "old_string": "foo", "new_string": "qux", "replace_all": true}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "2 occurrences") {
		t.Errorf("should report 2 replacements: %s", result)
	}

	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "foo") {
		t.Error("all 'foo' should be replaced")
	}
}

func TestHandleEditNotFound(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("hello world\n"), 0644)

	r := NewRegistry(dir)
	r.Handle(context.Background(), "read_file", toJSON(map[string]string{"path": "test.go"}))

	_, err := r.Handle(context.Background(), "edit_file",
		toJSON(map[string]interface{}{"path": "test.go", "old_string": "not here", "new_string": "x"}))
	if err == nil {
		t.Error("should fail when old_string not found")
	}
}

func TestHandleWrite(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)

	result, err := r.Handle(context.Background(), "write_file",
		toJSON(map[string]interface{}{"path": "new.txt", "content": "hello world"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Wrote") {
		t.Error("should report write")
	}

	content, _ := os.ReadFile(filepath.Join(dir, "new.txt"))
	if string(content) != "hello world" {
		t.Errorf("content=%q, want 'hello world'", string(content))
	}
}

func TestHandleWriteCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)

	_, err := r.Handle(context.Background(), "write_file",
		toJSON(map[string]interface{}{"path": "sub/dir/file.txt", "content": "nested"}))
	if err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filepath.Join(dir, "sub", "dir", "file.txt"))
	if string(content) != "nested" {
		t.Error("nested write failed")
	}
}

func TestHandleBash(t *testing.T) {
	r := NewRegistry(t.TempDir())
	result, err := r.Handle(context.Background(), "bash",
		toJSON(map[string]string{"command": "echo hello"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("result=%q, want 'hello'", result)
	}
}

func TestHandleBashFailure(t *testing.T) {
	r := NewRegistry(t.TempDir())
	result, err := r.Handle(context.Background(), "bash",
		toJSON(map[string]string{"command": "exit 1"}))
	// Should return result (with exit code), not error
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "exit") {
		t.Errorf("result=%q, should mention exit", result)
	}
}

func TestHandleBashTimeout(t *testing.T) {
	r := NewRegistry(t.TempDir())
	_, err := r.Handle(context.Background(), "bash",
		toJSON(map[string]interface{}{"command": "sleep 60", "timeout": 100}))
	if err == nil {
		t.Error("should timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error=%q, should mention timeout", err.Error())
	}
}

func TestHandleGlob(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte(""), 0644)

	r := NewRegistry(dir)
	result, err := r.Handle(context.Background(), "glob",
		toJSON(map[string]string{"pattern": "*.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "a.go") || !strings.Contains(result, "b.go") {
		t.Errorf("should find .go files: %s", result)
	}
	if strings.Contains(result, "c.txt") {
		t.Error("should not include .txt files")
	}
}

func TestHandleUnknownTool(t *testing.T) {
	r := NewRegistry(t.TempDir())
	_, err := r.Handle(context.Background(), "unknown_tool", toJSON(map[string]string{}))
	if err == nil {
		t.Error("should error on unknown tool")
	}
}

func TestPathConfinement(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)

	// Relative path that escapes workDir
	_, err := r.resolvePath("../../etc/passwd")
	if err == nil {
		t.Error("should reject relative path traversal escaping workDir")
	}

	// Absolute path outside workDir
	_, err = r.resolvePath("/etc/passwd")
	if err == nil {
		t.Error("should reject absolute path outside workDir")
	}

	// Valid relative path within workDir
	resolved, err := r.resolvePath("subdir/file.go")
	if err != nil {
		t.Fatalf("should accept valid relative path: %v", err)
	}
	expected := filepath.Join(dir, "subdir/file.go")
	if resolved != expected {
		t.Errorf("resolved=%q, want %q", resolved, expected)
	}

	// WorkDir itself is valid
	resolved, err = r.resolvePath(".")
	if err != nil {
		t.Fatalf("should accept workDir itself: %v", err)
	}
	if resolved != filepath.Clean(dir) {
		t.Errorf("resolved=%q, want %q", resolved, filepath.Clean(dir))
	}

	// Tools reject escaped paths at the handler level
	_, err = r.Handle(context.Background(), "read_file",
		toJSON(map[string]string{"path": "../../etc/passwd"}))
	if err == nil {
		t.Error("read_file should reject path escaping workDir")
	}
	if !strings.Contains(err.Error(), "escapes working directory") {
		t.Errorf("error should mention escaping: %v", err)
	}
}

func toJSON(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
