package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/agentloop"
)

func TestBuildNativeSupervisor_NoExpectations_ReturnsNil(t *testing.T) {
	fn := BuildNativeSupervisor(SupervisorConfig{WorkDir: "/tmp"})
	if fn != nil {
		t.Error("empty expectations should produce nil supervisor")
	}
}

func TestBuildNativeSupervisor_NoWorkDir_ReturnsNil(t *testing.T) {
	fn := BuildNativeSupervisor(SupervisorConfig{
		Expectations: []SpecExpectation{{File: "foo.rs", MustContain: []string{"x"}}},
	})
	if fn != nil {
		t.Error("empty workdir should produce nil supervisor")
	}
}

func TestBuildNativeSupervisor_QuietUntilThreshold(t *testing.T) {
	dir := t.TempDir()
	// File exists and is missing the required identifier — scan would
	// find a violation if it ran.
	os.WriteFile(filepath.Join(dir, "foo.rs"), []byte("fn nothing() {}"), 0o600)
	fn := BuildNativeSupervisor(SupervisorConfig{
		WorkDir:        dir,
		WritesPerCheck: 3,
		Expectations: []SpecExpectation{
			{File: "foo.rs", MustContain: []string{"pub struct Widget"}},
		},
	})
	if fn == nil {
		t.Fatal("expected non-nil fn")
	}

	// Simulate a single write_file tool call — below threshold.
	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "brief"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{
			{Type: "tool_use", ID: "t1", Name: "write_file"},
		}},
		{Role: "user", Content: []agentloop.ContentBlock{
			{Type: "tool_result", ToolUseID: "t1", Content: "Wrote foo.rs"},
		}},
	}
	if note := fn(msgs, 0); note != "" {
		t.Errorf("should be quiet below threshold, got: %s", note)
	}
}

func TestBuildNativeSupervisor_FiresAtThreshold(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "concern.rs"), []byte("struct GenericEntity{}"), 0o600)
	fn := BuildNativeSupervisor(SupervisorConfig{
		WorkDir:        dir,
		WritesPerCheck: 2,
		Expectations: []SpecExpectation{
			{File: "concern.rs", MustContain: []string{"pub struct Concern"}},
		},
	})

	// Feed 2 sequential write-producing turns — the second one should
	// fire the scan.
	for i := 0; i < 2; i++ {
		msgs := []agentloop.Message{
			{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "brief"}}},
			{Role: "assistant", Content: []agentloop.ContentBlock{
				{Type: "tool_use", ID: "t", Name: "write_file"},
			}},
		}
		if i == 1 {
			// After 2 writes, should return a note.
			note := fn(msgs, i)
			if note == "" {
				t.Fatal("expected a supervisor note at threshold")
			}
			if !strings.Contains(note, "MISSING canonical identifier") {
				t.Errorf("note should flag missing identifier:\n%s", note)
			}
			if !strings.Contains(note, "concern.rs") {
				t.Errorf("note should reference the file:\n%s", note)
			}
			if !strings.Contains(note, "pub struct Concern") {
				t.Errorf("note should include the expected identifier:\n%s", note)
			}
		} else {
			fn(msgs, i) // just accumulate
		}
	}
}

func TestBuildNativeSupervisor_DoesNotNagOnSameViolation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.rs"), []byte("// nope"), 0o600)
	fn := BuildNativeSupervisor(SupervisorConfig{
		WorkDir:        dir,
		WritesPerCheck: 1,
		Expectations: []SpecExpectation{
			{File: "a.rs", MustContain: []string{"pub struct A"}},
		},
	})

	makeMsgs := func() []agentloop.Message {
		return []agentloop.Message{
			{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "brief"}}},
			{Role: "assistant", Content: []agentloop.ContentBlock{
				{Type: "tool_use", ID: "t", Name: "write_file"},
			}},
		}
	}

	first := fn(makeMsgs(), 0)
	if first == "" {
		t.Fatal("expected note on first violation")
	}

	// Second call — same file still has the same violation. The
	// supervisor should NOT re-flag it.
	second := fn(makeMsgs(), 1)
	if second != "" {
		t.Errorf("should not re-nag about the same violation, got: %s", second)
	}
}

func TestBuildNativeSupervisor_IgnoresReadAndBash(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.rs"), []byte("nothing"), 0o600)
	fn := BuildNativeSupervisor(SupervisorConfig{
		WorkDir:        dir,
		WritesPerCheck: 1,
		Expectations: []SpecExpectation{
			{File: "x.rs", MustContain: []string{"must_exist"}},
		},
	})
	// read_file + bash — neither should count toward the write threshold.
	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "brief"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{
			{Type: "tool_use", ID: "t1", Name: "read_file"},
			{Type: "tool_use", ID: "t2", Name: "bash"},
		}},
	}
	if note := fn(msgs, 0); note != "" {
		t.Errorf("non-write tools should not trigger the scan, got: %s", note)
	}
}

func TestScanSpecExpectations_MissingFileSkipped(t *testing.T) {
	dir := t.TempDir()
	warned := map[string]bool{}
	violations := scanSpecExpectations(dir, []SpecExpectation{
		{File: "does-not-exist.rs", MustContain: []string{"foo"}},
	}, warned)
	if len(violations) != 0 {
		t.Errorf("missing file should not produce violations: %v", violations)
	}
}

func TestScanSpecExpectations_ForbiddenContent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "foo.rs"), []byte("pub struct WrongName;"), 0o600)
	violations := scanSpecExpectations(dir, []SpecExpectation{
		{File: "foo.rs", MustNotContain: []string{"WrongName"}},
	}, map[string]bool{})
	if len(violations) != 1 || len(violations[0].Forbidden) != 1 {
		t.Errorf("expected 1 forbidden violation: %+v", violations)
	}
}

func TestFormatSupervisorNote(t *testing.T) {
	note := formatSupervisorNote([]specViolation{
		{File: "a.rs", Missing: []string{"X", "Y"}, Forbidden: []string{"Z"}},
	})
	for _, want := range []string{"a.rs", "X", "Y", "Z", "MISSING canonical identifier", "FORBIDDEN"} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q:\n%s", want, note)
		}
	}
}

func TestPluralize(t *testing.T) {
	if pluralize(1, "file") != "1 file" {
		t.Error("pluralize(1) should not add s")
	}
	if pluralize(2, "file") != "2 files" {
		t.Error("pluralize(2) should add s")
	}
}
