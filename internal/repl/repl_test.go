package repl

import (
	"bufio"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/interview"
)

func TestNew(t *testing.T) {
	r := New("/tmp/test-repo")
	if r == nil {
		t.Fatal("New() returned nil")
	}
	if r.RepoRoot != "/tmp/test-repo" {
		t.Errorf("RepoRoot = %q, want %q", r.RepoRoot, "/tmp/test-repo")
	}
	if r.Commands == nil {
		t.Error("Commands map is nil")
	}
	if len(r.Commands) != 0 {
		t.Errorf("Commands has %d entries, want 0", len(r.Commands))
	}
}

func TestRegister(t *testing.T) {
	r := New("/tmp/repo")

	called := false
	r.Register(Command{
		Name:        "test",
		Description: "A test command",
		Usage:       "/test [args]",
		Run:         func(args string) { called = true },
	})

	if len(r.Commands) != 1 {
		t.Fatalf("Commands has %d entries, want 1", len(r.Commands))
	}

	cmd, ok := r.Commands["test"]
	if !ok {
		t.Fatal("command 'test' not found in Commands map")
	}
	if cmd.Name != "test" {
		t.Errorf("Name = %q, want %q", cmd.Name, "test")
	}
	if cmd.Description != "A test command" {
		t.Errorf("Description = %q, want %q", cmd.Description, "A test command")
	}
	if cmd.Usage != "/test [args]" {
		t.Errorf("Usage = %q, want %q", cmd.Usage, "/test [args]")
	}

	// Verify Run callback works
	cmd.Run("some args")
	if !called {
		t.Error("Run callback was not invoked")
	}
}

func TestRegister_Multiple(t *testing.T) {
	r := New("/tmp/repo")

	r.Register(Command{Name: "build", Description: "Build", Run: func(string) {}})
	r.Register(Command{Name: "scan", Description: "Scan", Run: func(string) {}})
	r.Register(Command{Name: "audit", Description: "Audit", Run: func(string) {}})

	if len(r.Commands) != 3 {
		t.Errorf("Commands has %d entries, want 3", len(r.Commands))
	}

	for _, name := range []string{"build", "scan", "audit"} {
		if _, ok := r.Commands[name]; !ok {
			t.Errorf("command %q not found", name)
		}
	}
}

func TestRegister_Overwrite(t *testing.T) {
	r := New("/tmp/repo")

	r.Register(Command{Name: "test", Description: "v1", Run: func(string) {}})
	r.Register(Command{Name: "test", Description: "v2", Run: func(string) {}})

	if len(r.Commands) != 1 {
		t.Errorf("Commands has %d entries, want 1 (overwrite)", len(r.Commands))
	}
	if r.Commands["test"].Description != "v2" {
		t.Errorf("Description = %q, want %q (overwrite)", r.Commands["test"].Description, "v2")
	}
}

// --- Run() loop tests using injectable Reader/Writer ---

// newTestREPL creates a REPL with injected input and captured output.
func newTestREPL(input string) (*REPL, *strings.Builder) {
	r := New("/tmp/test-repo")
	r.Reader = bufio.NewScanner(strings.NewReader(input))
	out := &strings.Builder{}
	r.Writer = out
	return r, out
}

func TestRunQuit(t *testing.T) {
	r, out := newTestREPL("/quit\n")
	r.Run()
	if !strings.Contains(out.String(), "Bye.") {
		t.Error("expected Bye. in output")
	}
}

func TestRunExitAlias(t *testing.T) {
	r, out := newTestREPL("/exit\n")
	r.Run()
	if !strings.Contains(out.String(), "Bye.") {
		t.Error("/exit should trigger quit")
	}
}

func TestRunQAlias(t *testing.T) {
	r, out := newTestREPL("/q\n")
	r.Run()
	if !strings.Contains(out.String(), "Bye.") {
		t.Error("/q should trigger quit")
	}
}

func TestRunBanner(t *testing.T) {
	r, out := newTestREPL("/quit\n")
	r.Run()
	s := out.String()
	if !strings.Contains(s, "STOKE") {
		t.Error("banner should contain STOKE")
	}
	if !strings.Contains(s, "test-repo") {
		t.Error("banner should contain repo path")
	}
}

func TestRunCommandDispatch(t *testing.T) {
	var gotArgs string
	r, _ := newTestREPL("/build src/main.go\n/quit\n")
	r.Register(Command{
		Name:        "build",
		Description: "Build the project",
		Run:         func(args string) { gotArgs = args },
	})
	r.Run()
	if gotArgs != "src/main.go" {
		t.Errorf("command args = %q, want %q", gotArgs, "src/main.go")
	}
}

func TestRunCommandNoArgs(t *testing.T) {
	var gotArgs string
	r, _ := newTestREPL("/scan\n/quit\n")
	r.Register(Command{
		Name: "scan",
		Run:  func(args string) { gotArgs = args },
	})
	r.Run()
	if gotArgs != "" {
		t.Errorf("command args = %q, want empty", gotArgs)
	}
}

func TestRunCommandCaseInsensitive(t *testing.T) {
	called := false
	r, _ := newTestREPL("/BUILD\n/quit\n")
	r.Register(Command{
		Name: "build",
		Run:  func(string) { called = true },
	})
	r.Run()
	if !called {
		t.Error("/BUILD should dispatch to build (case insensitive)")
	}
}

func TestRunUnknownCommand(t *testing.T) {
	r, out := newTestREPL("/foobar\n/quit\n")
	r.Run()
	if !strings.Contains(out.String(), "Unknown command: /foobar") {
		t.Error("expected unknown command message")
	}
}

func TestRunHelp(t *testing.T) {
	r, out := newTestREPL("/help\n/quit\n")
	r.Register(Command{
		Name:        "build",
		Description: "Build it",
		Usage:       "/build [target]",
		Run:         func(string) {},
	})
	r.Run()
	s := out.String()
	if !strings.Contains(s, "build") {
		t.Error("help should list build command")
	}
	if !strings.Contains(s, "/quit") {
		t.Error("help should mention /quit")
	}
}

func TestRunHelpQuestionMark(t *testing.T) {
	r, out := newTestREPL("/?\n/quit\n")
	r.Run()
	if !strings.Contains(out.String(), "/quit") {
		t.Error("/? should show help")
	}
}

func TestRunFreeTextChat(t *testing.T) {
	var chatInput string
	r, _ := newTestREPL("hello world\n/quit\n")
	r.OnChat = func(input string) { chatInput = input }
	r.Run()
	if chatInput != "hello world" {
		t.Errorf("OnChat got %q, want %q", chatInput, "hello world")
	}
}

func TestRunFreeTextNoChatHandler(t *testing.T) {
	r, out := newTestREPL("hello\n/quit\n")
	r.Run()
	if !strings.Contains(out.String(), "chat not configured") {
		t.Error("should warn when chat not configured")
	}
}

func TestRunEmptyLines(t *testing.T) {
	called := 0
	r, _ := newTestREPL("\n\n\n/build\n/quit\n")
	r.Register(Command{
		Name: "build",
		Run:  func(string) { called++ },
	})
	r.Run()
	if called != 1 {
		t.Errorf("build called %d times, want 1 (empty lines should be skipped)", called)
	}
}

func TestRunMultipleCommands(t *testing.T) {
	var order []string
	r, _ := newTestREPL("/a\n/b\n/c\n/quit\n")
	for _, name := range []string{"a", "b", "c"} {
		n := name
		r.Register(Command{Name: n, Run: func(string) { order = append(order, n) }})
	}
	r.Run()
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Errorf("order = %v, want [a b c]", order)
	}
}

func TestRunEOFExits(t *testing.T) {
	// No /quit — just EOF
	r, out := newTestREPL("hello\n")
	r.OnChat = func(string) {}
	r.Run()
	// Should exit cleanly without "Bye."
	if strings.Contains(out.String(), "Bye.") {
		t.Error("EOF exit should not print Bye")
	}
}

func TestRunBannerShowsRegisteredCommands(t *testing.T) {
	r, out := newTestREPL("/quit\n")
	r.Register(Command{Name: "ship", Description: "Ship it", Run: func(string) {}})
	r.Register(Command{Name: "build", Description: "Build it", Run: func(string) {}})
	r.Run()
	s := out.String()
	if !strings.Contains(s, "ship") || !strings.Contains(s, "build") {
		t.Error("banner should show registered commands")
	}
}

func TestRunSkillsCommand(t *testing.T) {
	r, out := newTestREPL("/skills\n/quit\n")
	r.RegisterBuiltins()
	r.Run()
	s := out.String()
	// Should list the embedded built-in skills
	if !strings.Contains(s, "skills available") {
		t.Error("should show skills count")
	}
	if !strings.Contains(s, "go-concurrency") {
		t.Error("should list go-concurrency skill")
	}
}

func TestRunSkillsSearchCommand(t *testing.T) {
	r, out := newTestREPL("/skills security\n/quit\n")
	r.RegisterBuiltins()
	r.Run()
	s := out.String()
	// Should match security-related skills
	if !strings.Contains(s, "security") {
		t.Error("should show security-related skills")
	}
}

func TestRunInterviewCommand(t *testing.T) {
	// Simulate: /interview fix the auth bug -> answer 3 questions -> done
	input := "/interview fix the auth bug\nMake login work\nskip\ndone\n/quit\n"
	r, out := newTestREPL(input)
	r.RegisterBuiltins()

	var receivedScope bool
	r.OnInterview = func(scope *interview.ClarifiedScope) {
		receivedScope = true
		if scope.OriginalRequest != "fix the auth bug" {
			t.Errorf("scope.OriginalRequest = %q", scope.OriginalRequest)
		}
	}

	r.Run()
	s := out.String()
	if !strings.Contains(s, "Deep Interview") {
		t.Error("should show Deep Interview header")
	}
	if !strings.Contains(s, "Clarified Scope") {
		t.Error("should show Clarified Scope")
	}
	if !receivedScope {
		t.Error("OnInterview callback was not called")
	}
}

func TestRunInterviewNoArgs(t *testing.T) {
	r, out := newTestREPL("/interview\n/quit\n")
	r.RegisterBuiltins()
	r.Run()
	if !strings.Contains(out.String(), "Usage: /interview") {
		t.Error("should show usage when no args")
	}
}

func TestPrintf(t *testing.T) {
	r := New("/tmp/repo")
	out := &strings.Builder{}
	r.Writer = out
	r.printf("hello %s %d", "world", 42)
	if out.String() != "hello world 42" {
		t.Errorf("printf output = %q", out.String())
	}
}

func TestPrintln(t *testing.T) {
	r := New("/tmp/repo")
	out := &strings.Builder{}
	r.Writer = out
	r.println("test line")
	if out.String() != "test line\n" {
		t.Errorf("println output = %q", out.String())
	}
}
