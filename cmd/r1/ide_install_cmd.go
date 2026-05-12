package main

// ide_install_cmd.go — `r1 ide …` subcommand group (spec
// specs/mcp-ide-bundles.md §T1, §T7, §T8, §T9, §T12.3).
//
// Three subcommands:
//
//   r1 ide install <cursor|windsurf|vscode|jetbrains>
//                       [--workspace PATH | --global] [--ide NAME] [--version VER]
//   r1 ide uninstall <cursor|windsurf|vscode|jetbrains>
//   r1 ide verify
//
// Exit codes (install):
//   0 success / 1 usage or IDE not detected / 2 config-merge conflict /
//   3 write failure / 4 JetBrains jar missing.
// Uninstall: 0 ok / 1 not installed.
// Verify: always 0.

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/ideinstall"
)

// ideRunner is the install-runner signature used by the dispatcher
// table. Extracted so tests can inject fakes via runnerLoader.
type ideRunner func(args []string, stdout, stderr io.Writer) int

// runnerLoader produces the per-IDE install runner for ide. Mirrors
// the registryLoader pattern in mcp.go so tests don't need to spawn
// real install flows. Returns an error when the IDE name is invalid.
type runnerLoader func(ide string) (ideRunner, error)

// defaultRunnerLoader maps the IDE name to the production runner.
// Falls through to an error returning the four valid names so
// operators can copy-paste the right command.
func defaultRunnerLoader(ide string) (ideRunner, error) {
	switch ide {
	case ideinstall.IDECursor:
		return runInstallCursor, nil
	case ideinstall.IDEWindsurf:
		return runInstallWindsurf, nil
	case ideinstall.IDEVSCode:
		return runInstallVSCode, nil
	case ideinstall.IDEJetBrains:
		return runInstallJetBrains, nil
	default:
		return nil, fmt.Errorf("unknown ide %q (valid: %s)",
			ide, strings.Join(ideinstall.SupportedIDEs, ", "))
	}
}

// ideCmd is the top-level dispatcher hooked up from main.go.
func ideCmd(args []string) {
	code := runIDECmd(args, os.Stdout, os.Stderr, defaultRunnerLoader)
	if code != 0 {
		os.Exit(code)
	}
}

const ideUsage = `r1 ide — install MCP server bundles for IDEs

USAGE:
  r1 ide install <cursor|windsurf|vscode|jetbrains> [--workspace PATH | --global]
                                                    [--ide NAME] [--version VER]
  r1 ide uninstall <cursor|windsurf|vscode|jetbrains>
  r1 ide verify

INSTALL FLAGS:
  --workspace PATH    install workspace-scope (Cursor, VS Code only)
  --global            install user-scope (default for Windsurf, JetBrains)
  --ide NAME          JetBrains only: IntelliJIdea | GoLand | PyCharm | WebStorm
  --version VER       JetBrains only: target version (default: highest detected)

EXIT CODES:
  0 success   1 usage / IDE not detected   2 config conflict
  3 write failure   4 JetBrains jar missing`

func runIDECmd(args []string, stdout, stderr io.Writer, loader runnerLoader) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, ideUsage)
		return 1
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "install":
		if len(rest) < 1 {
			fmt.Fprintln(stderr, "usage: r1 ide install <cursor|windsurf|vscode|jetbrains>")
			return 1
		}
		ide := rest[0]
		runner, err := loader(ide)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		return runner(rest[1:], stdout, stderr)
	case "uninstall":
		if len(rest) < 1 {
			fmt.Fprintln(stderr, "usage: r1 ide uninstall <cursor|windsurf|vscode|jetbrains>")
			return 1
		}
		return runUninstall(rest[0], stdout, stderr)
	case "verify":
		return runIDEVerify(rest, stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, ideUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown ide subcommand: %s\n\n%s\n", sub, ideUsage)
		return 1
	}
}

// resolveScope reads --workspace / --global from a FlagSet, applies
// the per-IDE default per spec §T1.2, and rejects mutually-exclusive
// combinations. Returns the resolved scope + the workspace dir.
type scopeFlags struct {
	workspace string
	global    bool
	wsSet     bool
}

func registerScopeFlags(fs *flag.FlagSet) *scopeFlags {
	sf := &scopeFlags{}
	fs.Func("workspace", "install workspace-scope (Cursor, VS Code only); accepts a path or '.'", func(v string) error {
		sf.workspace = v
		sf.wsSet = true
		return nil
	})
	fs.BoolVar(&sf.global, "global", false, "install user-scope (default for Windsurf, JetBrains)")
	return sf
}

func (sf *scopeFlags) resolve(ide string) (ideinstall.Scope, string, error) {
	if sf.wsSet && sf.global {
		return "", "", errors.New("--workspace and --global are mutually exclusive")
	}
	d, err := ideinstall.DetectorFor(ide)
	if err != nil {
		return "", "", err
	}
	switch {
	case sf.wsSet:
		if !d.SupportsScope(ideinstall.ScopeWorkspace) {
			return "", "", fmt.Errorf("%s does not support workspace scope", ide)
		}
		ws := sf.workspace
		if ws == "" || ws == "." {
			cwd, err := os.Getwd()
			if err != nil {
				return "", "", fmt.Errorf("resolve workspace dir: %w", err)
			}
			ws = cwd
		}
		abs, err := filepath.Abs(ws)
		if err != nil {
			return "", "", fmt.Errorf("resolve workspace dir: %w", err)
		}
		return ideinstall.ScopeWorkspace, abs, nil
	case sf.global:
		if !d.SupportsScope(ideinstall.ScopeGlobal) {
			return "", "", fmt.Errorf("%s does not support global scope", ide)
		}
		return ideinstall.ScopeGlobal, "", nil
	default:
		scope := ideinstall.DefaultScopeFor(ide)
		if scope == ideinstall.ScopeWorkspace {
			cwd, _ := os.Getwd()
			return ideinstall.ScopeWorkspace, cwd, nil
		}
		return ideinstall.ScopeGlobal, "", nil
	}
}

// --- per-IDE install runners --------------------------------------

func runInstallCursor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ide install cursor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sf := registerScopeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	scope, ws, err := sf.resolve(ideinstall.IDECursor)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	res, err := ideinstall.InstallCursor(scope, ws)
	if err != nil {
		return classifyInstallError(err, stderr)
	}
	ideinstall.PrintCursorConfirmation(stdout, res)
	return 0
}

func runInstallWindsurf(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ide install windsurf", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sf := registerScopeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if sf.wsSet {
		fmt.Fprintln(stderr, "error: windsurf does not support workspace scope")
		return 1
	}
	res, err := ideinstall.InstallWindsurf(stderr)
	if err != nil {
		return classifyInstallError(err, stderr)
	}
	ideinstall.PrintWindsurfConfirmation(stdout, res)
	return 0
}

func runInstallVSCode(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ide install vscode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sf := registerScopeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	scope, ws, err := sf.resolve(ideinstall.IDEVSCode)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	res, err := ideinstall.InstallVSCode(scope, ws)
	if err != nil {
		return classifyInstallError(err, stderr)
	}
	ideinstall.PrintVSCodeConfirmation(stdout, res)
	return 0
}

func runInstallJetBrains(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ide install jetbrains", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sf := registerScopeFlags(fs)
	ide := fs.String("ide", "IntelliJIdea",
		"JetBrains IDE: IntelliJIdea | GoLand | PyCharm | WebStorm")
	version := fs.String("version", "",
		"IDE version (e.g. 2026.2); empty = highest installed")
	skipDownload := fs.Bool("skip-download", false,
		"skip GitHub jar download fallback (returns exit 4 if no local jar found)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if sf.wsSet {
		fmt.Fprintln(stderr, "error: jetbrains does not support workspace scope")
		return 1
	}
	res, err := ideinstall.InstallJetBrains(ideinstall.InstallJetBrainsOpts{
		IDE:          *ide,
		Version:      *version,
		SkipDownload: *skipDownload,
	})
	if err != nil {
		// Missing jar (path A/B miss, network disabled) → exit 4.
		if strings.Contains(err.Error(), "not found") ||
			strings.Contains(err.Error(), "download disabled") ||
			strings.Contains(err.Error(), "download ") {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 4
		}
		return classifyInstallError(err, stderr)
	}
	ideinstall.PrintJetBrainsConfirmation(stdout, res, *ide)
	return 0
}

// classifyInstallError maps shared install errors to exit codes per
// spec §Public surface: write failure → 3, parse / conflict → 2,
// otherwise → 1.
func classifyInstallError(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "error: %v\n", err)
	msg := err.Error()
	switch {
	case strings.Contains(msg, "write config"),
		strings.Contains(msg, "write backup"),
		strings.Contains(msg, "write jar"),
		strings.Contains(msg, "copy jar"):
		return 3
	case strings.Contains(msg, "parse config"),
		strings.Contains(msg, "encode "):
		return 2
	default:
		return 1
	}
}

// runUninstall dispatches to the right per-IDE uninstaller and maps
// the result to exit codes (0 ok, 1 not installed).
func runUninstall(ide string, stdout, stderr io.Writer) int {
	res, err := ideinstall.Uninstall(ide)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	switch res.Action {
	case ideinstall.ActionRemoved:
		fmt.Fprintf(stdout, "uninstalled: r1 removed from %s\n", res.Path)
	case ideinstall.ActionRestored:
		fmt.Fprintf(stdout, "uninstalled: %s restored from backup\n", res.Path)
	default:
		fmt.Fprintf(stdout, "uninstalled: r1 removed from %s\n", res.Path)
	}
	return 0
}

// runIDEVerify renders the verify report. Always exits 0 per spec.
func runIDEVerify(_ []string, stdout, _ io.Writer) int {
	rows := ideinstall.Verify()
	ideinstall.PrintVerifyTable(stdout, rows)
	return 0
}

// --- First-run prompt (spec §T9) ----------------------------------

// idePromptAckFile is the path of the user-home ack file. Stored in
// the user's home rather than the per-project r1dir because the
// prompt itself is per-user (multiple repos must not re-prompt).
func idePromptAckFile() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".r1", "ide-prompt-acked"), nil
}

// promptInstaller is the dependency used by MaybePromptIDEInstall so
// tests can avoid running the real installer.
type promptInstaller func(ide string, stdout, stderr io.Writer) (ideinstall.Result, error)

// defaultPromptInstaller wires the prompt to the real install flow.
func defaultPromptInstaller(ide string, stdout, stderr io.Writer) (ideinstall.Result, error) {
	switch ide {
	case ideinstall.IDECursor:
		// Cursor defaults to workspace-scope, matching `r1 ide install
		// cursor` without flags.
		cwd, _ := os.Getwd()
		res, err := ideinstall.InstallCursor(ideinstall.ScopeWorkspace, cwd)
		if err == nil {
			ideinstall.PrintCursorConfirmation(stdout, res)
		}
		return res, err
	case ideinstall.IDEWindsurf:
		res, err := ideinstall.InstallWindsurf(stderr)
		if err == nil {
			ideinstall.PrintWindsurfConfirmation(stdout, res)
		}
		return res, err
	case ideinstall.IDEVSCode:
		cwd, _ := os.Getwd()
		res, err := ideinstall.InstallVSCode(ideinstall.ScopeWorkspace, cwd)
		if err == nil {
			ideinstall.PrintVSCodeConfirmation(stdout, res)
		}
		return res, err
	case ideinstall.IDEJetBrains:
		res, err := ideinstall.InstallJetBrains(ideinstall.InstallJetBrainsOpts{
			SkipDownload: true, // first-run prompt does not touch network
		})
		if err == nil {
			ideinstall.PrintJetBrainsConfirmation(stdout, res, "IntelliJIdea")
		}
		return res, err
	}
	return ideinstall.Result{}, fmt.Errorf("unknown ide %q", ide)
}

// PromptIDEOpts parameterises the first-run helper. interactive
// reflects whether stdin is a TTY; when false the helper is a no-op.
type PromptIDEOpts struct {
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Interactive bool                                                                                  // false → skip prompt entirely
	Verify      func() []ideinstall.VerifyResult                                                      // nil → ideinstall.Verify
	Installer   func(ide string, stdout, stderr io.Writer) (ideinstall.Result, error)                 // nil → defaultPromptInstaller
	AckFile     string                                                                                // empty → ~/.r1/ide-prompt-acked
	Clock       func() time.Time                                                                      // nil → time.Now
}

// MaybePromptIDEInstall asks the operator once whether they'd like to
// register R1 with the first unregistered IDE we detect. Returns
// `prompted` indicating whether we showed the prompt (false on
// non-TTY, ack-exists, or no unregistered IDE).
//
// State is recorded to opts.AckFile (default ~/.r1/ide-prompt-acked)
// in the format `<RFC3339>|<ide>|<accepted|declined>` per §T9.1.
func MaybePromptIDEInstall(opts PromptIDEOpts) (bool, error) {
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	verify := opts.Verify
	if verify == nil {
		verify = ideinstall.Verify
	}
	installer := opts.Installer
	if installer == nil {
		installer = defaultPromptInstaller
	}
	now := opts.Clock
	if now == nil {
		now = time.Now
	}

	ackPath := opts.AckFile
	if ackPath == "" {
		p, err := idePromptAckFile()
		if err != nil {
			return false, err
		}
		ackPath = p
	}
	if _, err := os.Stat(ackPath); err == nil {
		return false, nil
	}

	// Non-TTY → silent skip (spec §T9.2).
	if !opts.Interactive {
		return false, nil
	}

	rows := verify()
	var target string
	for _, r := range rows {
		if r.Found && !r.Registered {
			target = r.IDE
			break
		}
	}
	if target == "" {
		// No unregistered IDE — write an ack so we don't re-check
		// every chat session.
		_ = writeAck(ackPath, now(), "none", "none")
		return false, nil
	}

	fmt.Fprintf(stdout, "R1 detected %s — install R1 MCP server in %s? [y/N] ",
		target, target)
	br := bufio.NewReader(stdin)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		// Stdin closed without input → treat as decline, write ack.
		_ = writeAck(ackPath, now(), target, "declined")
		return true, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	switch answer {
	case "y", "yes":
		if _, err := installer(target, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "warning: install failed: %v\n", err)
			_ = writeAck(ackPath, now(), target, "declined")
			return true, err
		}
		_ = writeAck(ackPath, now(), target, "accepted")
	default:
		_ = writeAck(ackPath, now(), target, "declined")
	}
	return true, nil
}

// writeAck writes the ack line in the format
// `<RFC3339-timestamp>|<ide>|<accepted|declined|none>` to ackPath.
// The directory is created if needed.
func writeAck(ackPath string, when time.Time, ide, status string) error {
	if err := os.MkdirAll(filepath.Dir(ackPath), 0o755); err != nil {
		return err
	}
	line := fmt.Sprintf("%s|%s|%s\n", when.UTC().Format(time.RFC3339), ide, status)
	return os.WriteFile(ackPath, []byte(line), 0o644)
}
