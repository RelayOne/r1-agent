// review_test.go — tests for the cross-adapter shared review primitives.
package shared

import (
	"strings"
	"testing"
)

func TestCommitStatusNameStable(t *testing.T) {
	// The byte-for-byte value is part of the C5 parity contract — every
	// adapter writes this exact string as the head-commit status row name.
	if CommitStatusName != "R1 Verify" {
		t.Errorf("CommitStatusName = %q, want %q", CommitStatusName, "R1 Verify")
	}
}

func TestRenderCommentBodyFormat(t *testing.T) {
	cases := []struct {
		name string
		f    Finding
		want string
	}{
		{
			name: "warning with body",
			f:    Finding{Severity: "warning", Body: "unchecked error"},
			want: "**[r1-review · WARNING]** unchecked error",
		},
		{
			name: "error severity uppercased",
			f:    Finding{Severity: "error", Body: "nil deref risk"},
			want: "**[r1-review · ERROR]** nil deref risk",
		},
		{
			name: "missing severity defaults to INFO",
			f:    Finding{Body: "consider extracting helper"},
			want: "**[r1-review · INFO]** consider extracting helper",
		},
		{
			name: "body trimmed",
			f:    Finding{Severity: "info", Body: "  spaces around  \n"},
			want: "**[r1-review · INFO]** spaces around",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderCommentBody(tc.f)
			if got != tc.want {
				t.Errorf("\nwant: %q\ngot:  %q", tc.want, got)
			}
		})
	}
}

func TestParseFindingsReadsBullets(t *testing.T) {
	response := `Here are the findings I see:

- **warning** main.go:42 — handle the error returned from os.Open
- **error** util.go:7 — possible nil dereference on *cfg
- **info** util.go:99 — consider extracting this into a helper
- not a finding line, ignored
- **bogus** x.go:1 — unknown severity becomes info

That's it.`

	got := ParseFindings(response)
	if len(got) != 4 {
		t.Fatalf("findings = %d, want 4: %+v", len(got), got)
	}

	check := func(i int, path string, line int, sev string) {
		t.Helper()
		if got[i].Path != path {
			t.Errorf("got[%d].Path = %q, want %q", i, got[i].Path, path)
		}
		if got[i].Line != line {
			t.Errorf("got[%d].Line = %d, want %d", i, got[i].Line, line)
		}
		if got[i].Severity != sev {
			t.Errorf("got[%d].Severity = %q, want %q", i, got[i].Severity, sev)
		}
	}
	check(0, "main.go", 42, "warning")
	check(1, "util.go", 7, "error")
	check(2, "util.go", 99, "info")
	check(3, "x.go", 1, "info")
}

func TestParseFindingsNoneSentinel(t *testing.T) {
	for _, response := range []string{"NO FINDINGS", "Looks good. NO FINDINGS to report."} {
		if got := ParseFindings(response); len(got) != 0 {
			t.Errorf("response %q produced findings: %+v", response, got)
		}
	}
}

func TestFindingValidityGate(t *testing.T) {
	if !(Finding{Path: "x", Line: 1, Body: "ok"}).IsValid() {
		t.Error("valid finding should pass IsValid")
	}
	bad := []Finding{
		{Line: 1, Body: "ok"},
		{Path: "x", Body: "ok"},
		{Path: "x", Line: -1, Body: "ok"},
		{Path: "x", Line: 1},
		{Path: "x", Line: 1, Body: "   \t\n "},
	}
	for i, f := range bad {
		if f.IsValid() {
			t.Errorf("case %d: %+v should be invalid", i, f)
		}
	}
}

func TestRenderPromptSubstitutesDiff(t *testing.T) {
	got := RenderPrompt("REVIEW THIS:\n{{DIFF}}\nDONE.", "fake-diff")
	want := "REVIEW THIS:\nfake-diff\nDONE."
	if got != want {
		t.Errorf("RenderPrompt = %q, want %q", got, want)
	}
}

func TestRenderPromptFallsBackToDefault(t *testing.T) {
	got := RenderPrompt("", "fake-diff")
	if !strings.Contains(got, "fake-diff") {
		t.Errorf("default prompt did not embed diff: %q", got)
	}
	if !strings.Contains(got, "NO FINDINGS") {
		t.Errorf("default prompt missing NO FINDINGS sentinel")
	}
}

func TestModeConstants(t *testing.T) {
	// Lock the canonical mode-string values down so an adapter cannot
	// silently drift from the parent cicd package.
	if ModeReview != "review" || ModeAutoFix != "autofix" || ModeMission != "mission" {
		t.Errorf("mode constants drift: %q %q %q", ModeReview, ModeAutoFix, ModeMission)
	}
}
