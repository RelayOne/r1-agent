package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ideinstall"
)

// stubRunner records the invocation and returns a fixed exit code.
type stubRunner struct {
	calls []string
	code  int
}

func (s *stubRunner) makeLoader() runnerLoader {
	return func(ide string) (ideRunner, error) {
		switch ide {
		case ideinstall.IDECursor, ideinstall.IDEWindsurf,
			ideinstall.IDEVSCode, ideinstall.IDEJetBrains:
			return func(args []string, _, _ io.Writer) int {
				s.calls = append(s.calls, ide+":"+strings.Join(args, ","))
				return s.code
			}, nil
		}
		return nil, &ideError{ide: ide}
	}
}

type ideError struct{ ide string }

func (e *ideError) Error() string {
	return "unknown ide \"" + e.ide + "\""
}

func TestRunIDECmd_NoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	s := &stubRunner{}
	code := runIDECmd(nil, &stdout, &stderr, s.makeLoader())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "r1 ide") {
		t.Fatalf("usage missing: %s", stderr.String())
	}
}

func TestRunIDECmd_HelpExitsZero(t *testing.T) {
	for _, flag := range []string{"-h", "--help", "help"} {
		var stdout, stderr bytes.Buffer
		s := &stubRunner{}
		code := runIDECmd([]string{flag}, &stdout, &stderr, s.makeLoader())
		if code != 0 {
			t.Fatalf("[%s] code = %d, want 0", flag, code)
		}
		if !strings.Contains(stdout.String(), "r1 ide") {
			t.Fatalf("[%s] usage missing: %s", flag, stdout.String())
		}
	}
}

func TestRunIDECmd_DispatchInstall(t *testing.T) {
	for _, ide := range []string{"cursor", "windsurf", "vscode", "jetbrains"} {
		var stdout, stderr bytes.Buffer
		s := &stubRunner{}
		code := runIDECmd([]string{"install", ide, "--flag1"}, &stdout, &stderr, s.makeLoader())
		if code != 0 {
			t.Fatalf("[%s] code = %d, want 0; stderr=%s", ide, code, stderr.String())
		}
		if len(s.calls) != 1 {
			t.Fatalf("[%s] calls = %d, want 1", ide, len(s.calls))
		}
		if !strings.HasPrefix(s.calls[0], ide+":") {
			t.Fatalf("[%s] dispatched to wrong runner: %s", ide, s.calls[0])
		}
	}
}

func TestRunIDECmd_InstallUnknownIDE(t *testing.T) {
	var stdout, stderr bytes.Buffer
	s := &stubRunner{}
	code := runIDECmd([]string{"install", "eclipse"}, &stdout, &stderr, s.makeLoader())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "eclipse") {
		t.Fatalf("error missing IDE name: %s", stderr.String())
	}
}

func TestRunIDECmd_InstallMissingIDEName(t *testing.T) {
	var stdout, stderr bytes.Buffer
	s := &stubRunner{}
	code := runIDECmd([]string{"install"}, &stdout, &stderr, s.makeLoader())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRunIDECmd_VerifyExitsZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var stdout, stderr bytes.Buffer
	s := &stubRunner{}
	code := runIDECmd([]string{"verify"}, &stdout, &stderr, s.makeLoader())
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, stderr.String())
	}
	// All four IDEs must appear in the table.
	for _, ide := range []string{"cursor", "windsurf", "vscode", "jetbrains"} {
		if !strings.Contains(stdout.String(), ide) {
			t.Fatalf("verify output missing %s: %s", ide, stdout.String())
		}
	}
}

func TestRunIDECmd_UninstallMissingIDEName(t *testing.T) {
	var stdout, stderr bytes.Buffer
	s := &stubRunner{}
	code := runIDECmd([]string{"uninstall"}, &stdout, &stderr, s.makeLoader())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestScopeFlags_MutuallyExclusive(t *testing.T) {
	// Drive runInstallCursor with both --workspace and --global to
	// confirm the mutually-exclusive guard fires.
	var stdout, stderr bytes.Buffer
	code := runInstallCursor([]string{"--workspace", ".", "--global"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("missing mutually-exclusive error: %s", stderr.String())
	}
}

func TestScopeFlags_WindsurfRejectsWorkspace(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runInstallWindsurf([]string{"--workspace", "."}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "workspace") {
		t.Fatalf("missing workspace rejection: %s", stderr.String())
	}
}

func TestRunInstallCursor_Workspace(t *testing.T) {
	ws := t.TempDir()
	// LINT-ALLOW chdir-test: test-only cwd capture; restores via defer.
	prev, _ := os.Getwd()
	// LINT-ALLOW chdir-test: scoped cwd switch within a test.
	defer func() { _ = os.Chdir(prev) }()
	// LINT-ALLOW chdir-test: scoped cwd switch within a test.
	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := runInstallCursor([]string{"--workspace", "."}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(ws, ".cursor", "mcp.json")); err != nil {
		t.Fatalf("config not written: %v", err)
	}
}

func TestRunInstallJetBrains_SkipDownloadExits4(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("R1_JETBRAINS_JAR", "")
	// chdir somewhere with no checked-in jar to make the
	// developer-checkout path search miss.
	wd := t.TempDir()
	// LINT-ALLOW chdir-test: test-only cwd capture; restores via defer.
	prev, _ := os.Getwd()
	// LINT-ALLOW chdir-test: scoped cwd switch within a test.
	defer func() { _ = os.Chdir(prev) }()
	// LINT-ALLOW chdir-test: scoped cwd switch within a test.
	_ = os.Chdir(wd)
	var stdout, stderr bytes.Buffer
	code := runInstallJetBrains(
		[]string{"--ide", "IntelliJIdea", "--version", "2026.1", "--skip-download"},
		&stdout, &stderr)
	if code != 4 {
		t.Fatalf("code = %d, want 4; stderr=%s", code, stderr.String())
	}
}

func TestMaybePromptIDEInstall_AckSkipsPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ack := filepath.Join(home, ".r1", "ide-prompt-acked")
	if err := os.MkdirAll(filepath.Dir(ack), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(ack, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	verifyCalled := false
	prompted, err := MaybePromptIDEInstall(PromptIDEOpts{
		Stdin:       strings.NewReader("y\n"),
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Interactive: true,
		Verify: func() []ideinstall.VerifyResult {
			verifyCalled = true
			return nil
		},
		AckFile: ack,
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if prompted {
		t.Fatalf("expected silent return when ack exists")
	}
	if verifyCalled {
		t.Fatalf("Verify must not run when ack exists")
	}
}

func TestMaybePromptIDEInstall_NonTTYSkips(t *testing.T) {
	home := t.TempDir()
	ack := filepath.Join(home, "ide-prompt-acked")
	var verifyCalled bool
	prompted, err := MaybePromptIDEInstall(PromptIDEOpts{
		Stdin:       strings.NewReader("y\n"),
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Interactive: false, // non-TTY
		Verify: func() []ideinstall.VerifyResult {
			verifyCalled = true
			return nil
		},
		AckFile: ack,
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if prompted {
		t.Fatalf("non-TTY must not prompt")
	}
	if _, err := os.Stat(ack); err == nil {
		t.Fatalf("non-TTY must not write ack")
	}
	if verifyCalled {
		t.Fatalf("non-TTY must not call Verify")
	}
}

func TestMaybePromptIDEInstall_AcceptInstalls(t *testing.T) {
	home := t.TempDir()
	ack := filepath.Join(home, "ide-prompt-acked")
	var installed string
	verify := func() []ideinstall.VerifyResult {
		return []ideinstall.VerifyResult{
			{IDE: "cursor", Found: true, Registered: false, Status: ideinstall.VerifyStatusUnregistered},
		}
	}
	installer := func(ide string, _, _ io.Writer) (ideinstall.Result, error) {
		installed = ide
		return ideinstall.Result{Path: "/x", Action: ideinstall.ActionAdded}, nil
	}
	prompted, err := MaybePromptIDEInstall(PromptIDEOpts{
		Stdin:       strings.NewReader("y\n"),
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Interactive: true,
		Verify:      verify,
		Installer:   installer,
		AckFile:     ack,
		Clock:       func() time.Time { return time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if !prompted {
		t.Fatalf("expected prompted=true")
	}
	if installed != "cursor" {
		t.Fatalf("installed = %q, want cursor", installed)
	}
	got, err := os.ReadFile(ack)
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	if !strings.Contains(string(got), "cursor|accepted") {
		t.Fatalf("ack contents = %q, want cursor|accepted", got)
	}
}

func TestMaybePromptIDEInstall_DeclineRecordsDeclined(t *testing.T) {
	home := t.TempDir()
	ack := filepath.Join(home, "ide-prompt-acked")
	var installCalled bool
	verify := func() []ideinstall.VerifyResult {
		return []ideinstall.VerifyResult{
			{IDE: "cursor", Found: true, Registered: false, Status: ideinstall.VerifyStatusUnregistered},
		}
	}
	installer := func(_ string, _, _ io.Writer) (ideinstall.Result, error) {
		installCalled = true
		return ideinstall.Result{}, nil
	}
	_, err := MaybePromptIDEInstall(PromptIDEOpts{
		Stdin:       strings.NewReader("n\n"),
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Interactive: true,
		Verify:      verify,
		Installer:   installer,
		AckFile:     ack,
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if installCalled {
		t.Fatalf("decline must not install")
	}
	got, _ := os.ReadFile(ack)
	if !strings.Contains(string(got), "cursor|declined") {
		t.Fatalf("ack = %q, want cursor|declined", got)
	}
}

func TestMaybePromptIDEInstall_NoUnregisteredWritesNoneAck(t *testing.T) {
	home := t.TempDir()
	ack := filepath.Join(home, "ide-prompt-acked")
	verify := func() []ideinstall.VerifyResult {
		return []ideinstall.VerifyResult{
			{IDE: "cursor", Found: true, Registered: true, Status: ideinstall.VerifyStatusRegistered},
		}
	}
	prompted, err := MaybePromptIDEInstall(PromptIDEOpts{
		Stdin:       strings.NewReader(""),
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Interactive: true,
		Verify:      verify,
		AckFile:     ack,
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if prompted {
		t.Fatalf("must not prompt when nothing to install")
	}
	got, _ := os.ReadFile(ack)
	if !strings.Contains(string(got), "none|none") {
		t.Fatalf("ack = %q, want none|none", got)
	}
}
