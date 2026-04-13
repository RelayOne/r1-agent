// Package plan provides the live build-watcher daemon.
//
// WHY: stoke historically ran acceptance / build commands only in a
// batched "Phase 2" step after every parallel worker in a session had
// already finished. If Worker A introduced a compile error in Phase 1,
// stoke would not notice until every sibling worker had also completed;
// the whole repair cycle then had to unwind damage that could have been
// caught the moment the broken file hit disk.
//
// CC and Codex keep a compiler in --watch mode for the duration of a
// conversation so the operator sees type / compile errors in real time.
// BuildWatcher gives stoke the same behaviour — but it does not hand the
// raw stderr to an LLM. Instead the subprocess's stderr is parsed by
// deterministic per-stack regex into a structured CompileError queue.
// That queue is the ground truth: workers consult SummaryForPrompt() so
// their prompts include the live list of compile errors, and the per-
// task reviewer filters the queue to the files the task touched and
// treats those findings as authoritative in-scope gaps (no LLM
// re-evaluation of "is this really an error?" — tsc says it's an error,
// therefore it is).
//
// Supervised + deterministic:
//   - Stack detection feeds in via ExecutorKind (not LLM judgement).
//   - Parsing is regex per stack (not LLM interpretation).
//   - Dedup / aging is structural (File+Line+Col+Code key).
//   - Workers cannot "skip" errors — the reviewer gates them.
package plan

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Watcher-specific ExecutorKind constants. These identify the
// build/type-check tool driving the watcher. They intentionally do not
// collide with the hygiene ExecutorKind values (pnpm/npm/pip/...) — the
// watcher cares about the *compiler*, hygiene cares about the *package
// manager*. Callers translate from SOW stack language via
// WatcherKindForLanguage.
const (
	// ExecTS drives the TypeScript compiler in watch mode.
	ExecTS ExecutorKind = "ts"
	// ExecPyright drives the pyright type-checker in watch mode.
	ExecPyright ExecutorKind = "pyright"
)

// WatcherKindForLanguage maps a SOW stack.language string to the
// ExecutorKind the BuildWatcher understands. Returns empty when no
// meaningful watcher exists for the language; callers treat empty as
// "skip the watcher".
func WatcherKindForLanguage(lang string) ExecutorKind {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "typescript", "javascript", "ts", "js":
		return ExecTS
	case "go", "golang":
		return ExecGoMod
	case "rust":
		return ExecCargo
	case "python", "py":
		return ExecPyright
	}
	return ""
}

// CompileError is one structured finding from the live build watcher.
// Fields mirror what tsc / rustc / go / pyright emit.
type CompileError struct {
	// File is a path relative to the watcher's repoRoot.
	File string
	// Line is 1-based. 0 if the tool did not emit a line number.
	Line int
	// Column is 1-based. 0 if the tool did not emit a column.
	Column int
	// Code is the compiler's error code (e.g. "TS2322", "E0308"). May
	// be empty for stacks that don't attach codes (go build).
	Code string
	// Message is the human-readable error text.
	Message string
	// Stack identifies which executor produced it (ts, go, cargo,
	// pyright). Matches the ExecutorKind the watcher was built with.
	Stack string
	// Seen is when the watcher first observed this error in the
	// current active window.
	Seen time.Time
}

// key is the dedup key. Errors with the same File+Line+Column+Code are
// the same error for queue purposes.
func (e CompileError) key() string {
	return fmt.Sprintf("%s|%d|%d|%s", e.File, e.Line, e.Column, e.Code)
}

// maxTrackedErrors caps the authoritative list. tsc can produce
// thousands of errors when a single base type goes bad; we don't want
// to inject all of them into every prompt.
const maxTrackedErrors = 200

// staleAfter drops errors the watcher hasn't re-observed within this
// window when the stack does not emit a clean-boundary signal. tsc's
// "Watching for file changes" resets the window authoritatively; for
// stacks without that signal, time-based aging is the fallback.
const staleAfter = 30 * time.Second

// watcherMaxRestarts caps how many times we relaunch a dying watch
// subprocess before giving up. After this, the watcher is dead and
// Current() returns empty. Crash-looping forever would waste CPU and
// mask the underlying problem.
const watcherMaxRestarts = 3

// BuildWatcher is a continuous compile check. It runs the stack's
// watch-mode command (tsc --watch, cargo check --watch, go build on
// file change, pyright --watch) as a subprocess, parses stderr into
// CompileError entries, and keeps a deduped authoritative queue workers
// can consult at any time.
//
// Not thread-safe to Start twice on the same instance. Safe to call
// Current(), SummaryForPrompt() and Stop() from any goroutine.
type BuildWatcher struct {
	repoRoot string
	stack    ExecutorKind

	mu         sync.Mutex
	errors     map[string]CompileError // keyed by CompileError.key()
	cmd        *exec.Cmd
	started    bool
	dead       bool
	ctxCancel  context.CancelFunc
	doneCh     chan struct{}
	restarts   int
	lastUpdate time.Time
	// cycleStart marks the instant the CURRENT compile cycle began
	// (set by the parser when it sees a boundary sentinel like tsc's
	// "Starting compilation in watch mode" or a polling-mode
	// invocation's first line). ageOutAtBoundary drops every error
	// whose Seen < cycleStart — so a clean cycle (no new errors) DOES
	// clear the prior cycle's diagnostics instead of keeping them
	// "authoritative" forever.
	cycleStart time.Time
}

// NewBuildWatcher constructs a watcher for repoRoot. stack selects the
// watch command. Returns nil when the stack has no meaningful watch
// mode — callers should treat nil as "no live watcher, no-op".
func NewBuildWatcher(repoRoot string, stack ExecutorKind) *BuildWatcher {
	if repoRoot == "" {
		return nil
	}
	if watchCommandFor(stack) == nil {
		return nil
	}
	return &BuildWatcher{
		repoRoot: repoRoot,
		stack:    stack,
		errors:   map[string]CompileError{},
		doneCh:   make(chan struct{}),
	}
}

// Start launches the watch subprocess and begins parsing its stderr.
// Idempotent — a second call returns nil without starting another
// subprocess. The watcher runs until Stop() or ctx cancellation.
//
// Start does not block. The subprocess, parser and restart-supervisor
// run on background goroutines.
func (w *BuildWatcher) Start(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return nil
	}
	w.started = true
	childCtx, cancel := context.WithCancel(ctx)
	w.ctxCancel = cancel
	w.mu.Unlock()

	go w.supervise(childCtx)
	return nil
}

// Stop terminates the subprocess and all watcher goroutines. Safe to
// call multiple times. After Stop, Current() continues to return the
// snapshot observed at the moment of termination until the caller
// discards the watcher.
func (w *BuildWatcher) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	cancel := w.ctxCancel
	w.ctxCancel = nil
	cmd := w.cmd
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cmd != nil {
		killWatcherProcessGroup(cmd)
	}
}

// Current returns a snapshot of the authoritative error list at this
// moment. The returned slice is owned by the caller (safe to mutate).
// Empty slice = compile clean OR watcher dead.
func (w *BuildWatcher) Current() []CompileError {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]CompileError, 0, len(w.errors))
	for _, e := range w.errors {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Column < out[j].Column
	})
	return out
}

// SummaryForPrompt returns a compact string suitable for injection into
// worker prompts. Returns "" when clean. Truncates to 10 errors with
// "...and N more" suffix.
func (w *BuildWatcher) SummaryForPrompt() string {
	errs := w.Current()
	if len(errs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "LIVE BUILD STATE: %d compile error(s) currently in the repo:\n", len(errs))
	limit := 10
	for i, e := range errs {
		if i >= limit {
			fmt.Fprintf(&b, "  ...and %d more\n", len(errs)-limit)
			break
		}
		codePart := ""
		if e.Code != "" {
			codePart = " [" + e.Code + "]"
		}
		if e.Line > 0 && e.Column > 0 {
			fmt.Fprintf(&b, "  - %s:%d:%d%s %s\n", e.File, e.Line, e.Column, codePart, e.Message)
		} else if e.Line > 0 {
			fmt.Fprintf(&b, "  - %s:%d%s %s\n", e.File, e.Line, codePart, e.Message)
		} else {
			fmt.Fprintf(&b, "  - %s%s %s\n", e.File, codePart, e.Message)
		}
	}
	return b.String()
}

// FilterToFiles returns the subset of the current error list whose File
// matches any entry in paths (matched by suffix so task.Files entries
// relative to repoRoot line up with watcher entries that may include a
// leading "./"). Empty paths returns an empty slice.
func (w *BuildWatcher) FilterToFiles(paths []string) []CompileError {
	if w == nil || len(paths) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimPrefix(p, "./")
		if p != "" {
			normalized = append(normalized, p)
		}
	}
	all := w.Current()
	out := make([]CompileError, 0, len(all))
	for _, e := range all {
		ef := strings.TrimPrefix(e.File, "./")
		for _, p := range normalized {
			if ef == p || strings.HasSuffix(ef, "/"+p) || strings.HasSuffix(p, "/"+ef) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// supervise runs the subprocess loop. Behavior depends on the
// command's mode:
//
//   - watchModeStreaming: long-lived subprocess (tsc --watch). Clean
//     exit bumps the restart counter; watcher dies after
//     watcherMaxRestarts unexpected exits.
//   - watchModePolling: one-shot subprocess (go build, cargo check).
//     Clean exit marks cycle completion; supervisor ages out the
//     previous cycle's errors, sleeps pollInterval, and re-runs. The
//     restart counter only trips on start failures (binary missing,
//     etc.) that runOnce reports as a non-graceful error.
func (w *BuildWatcher) supervise(ctx context.Context) {
	defer close(w.doneCh)
	spec := watchCommandFor(w.stack)
	polling := spec != nil && spec.mode == watchModePolling
	for {
		if ctx.Err() != nil {
			return
		}
		// Begin-of-cycle marker so ageOut at end of cycle drops
		// errors not re-seen in this iteration.
		w.beginCycle()
		startErr := w.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if polling {
			// Exit — whether clean or with compile errors — marks
			// the end of this cycle. Drop stale errors from the
			// previous cycle's queue that we did not re-observe.
			w.ageOut(0)
			// startErr is typically nil (non-zero compile exit is
			// expected) OR an actual start failure (binary not
			// found). Only the latter should bump the restart
			// counter to avoid infinite looping on "go: not found".
			if startErr != nil && w.startFailure(startErr) {
				w.mu.Lock()
				w.restarts++
				tooMany := w.restarts > watcherMaxRestarts
				if tooMany {
					w.dead = true
					w.errors = map[string]CompileError{}
				}
				w.mu.Unlock()
				if tooMany {
					return
				}
			}
			interval := spec.pollInterval
			if interval <= 0 {
				interval = 15 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			continue
		}
		// Streaming mode: exit is unexpected. Bump restart budget.
		w.mu.Lock()
		w.restarts++
		if w.restarts > watcherMaxRestarts {
			w.dead = true
			w.errors = map[string]CompileError{}
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
		_ = startErr
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// startFailure reports whether err from runOnce indicates the
// subprocess failed to START (binary missing, permission denied)
// as opposed to running-then-exiting. Polling-mode uses this to
// decide whether to bump the restart budget: a compile-error exit
// is normal; a start failure is not.
func (w *BuildWatcher) startFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "permission denied")
}

// runOnce starts the watch subprocess, reads its stderr+stdout, and
// returns when the process exits. Returned error is informational only
// (supervise() decides whether to relaunch based on ctx state).
func (w *BuildWatcher) runOnce(ctx context.Context) error {
	spec := watchCommandFor(w.stack)
	if spec == nil {
		return fmt.Errorf("no watch command for stack %q", w.stack)
	}

	cmd := exec.CommandContext(ctx, spec.program, spec.args...)
	cmd.Dir = w.repoRoot
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		// Typically "executable not found" — graceful fallback: mark
		// dead so restart loop bails, and keep an empty queue.
		w.mu.Lock()
		w.dead = true
		w.mu.Unlock()
		return fmt.Errorf("start %s: %w", spec.program, err)
	}

	w.mu.Lock()
	w.cmd = cmd
	w.mu.Unlock()

	parser := newWatchParser(string(w.stack))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		w.consumeStream(stdout, parser)
	}()
	go func() {
		defer wg.Done()
		w.consumeStream(stderr, parser)
	}()
	wg.Wait()
	waitErr := cmd.Wait()

	w.mu.Lock()
	w.cmd = nil
	// Do NOT blanket-clear errors here. In polling mode (go/cargo)
	// the subprocess legitimately exits between cycles — consumers
	// must still be able to read the last cycle's compile errors
	// during the pollInterval window. Stale-error removal is driven
	// by ageOut at the next cycle start, not by process exit.
	// Streaming mode (tsc/pyright) also benefits: an accidental
	// subprocess restart in the middle of a session no longer drops
	// the error set the reviewer was just about to consult.
	w.mu.Unlock()
	return waitErr
}

// consumeStream reads one of the subprocess's streams line-by-line and
// feeds each line into the parser. When the parser signals a
// batch-boundary (e.g. tsc's "Watching for file changes") the watcher
// rotates its error set: anything not seen in the current batch ages
// out.
func (w *BuildWatcher) consumeStream(r io.Reader, parser *watchParser) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		boundary, batch := parser.Feed(line)
		if len(batch) > 0 {
			w.ingest(batch)
		}
		if boundary {
			// Drop any error not re-seen in the cycle that just
			// ended, then mark the next cycle's start instant.
			// Errors re-emitted after beginCycle fires will survive
			// the next ageOut; stale ones won't.
			w.ageOut(parser.boundaryCount())
			w.beginCycle()
		}
	}
	// On EOF, flush any pending errors the parser accumulated.
	if pending := parser.Flush(); len(pending) > 0 {
		w.ingest(pending)
	}
}

// ingest merges a batch of freshly-parsed errors into the authoritative
// map. Existing keys get their Seen time refreshed; new keys are added
// up to maxTrackedErrors (oldest-by-Seen drops when exceeded).
func (w *BuildWatcher) ingest(batch []CompileError) {
	if len(batch) == 0 {
		return
	}
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, e := range batch {
		if e.File == "" {
			continue
		}
		e.Seen = now
		w.errors[e.key()] = e
	}
	w.lastUpdate = now
	if len(w.errors) > maxTrackedErrors {
		// Drop oldest by Seen until we're back under the cap.
		type kv struct {
			k string
			t time.Time
		}
		list := make([]kv, 0, len(w.errors))
		for k, v := range w.errors {
			list = append(list, kv{k, v.Seen})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].t.Before(list[j].t) })
		drop := len(w.errors) - maxTrackedErrors
		for i := 0; i < drop; i++ {
			delete(w.errors, list[i].k)
		}
	}
}

// ageOut is called on authoritative boundary signals (e.g. tsc's
// "Watching for file changes", cargo's "Finished", a polling-mode
// cycle end). Every error whose Seen timestamp predates the current
// cycleStart is dropped — so a CLEAN cycle (parser saw boundary,
// then no new errors, then next boundary) DOES clear the prior
// cycle's diagnostics.
//
// For stacks without a boundary signal the time-based fallback in
// pruneStale handles aging.
func (w *BuildWatcher) ageOut(_ int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := w.cycleStart
	if cutoff.IsZero() {
		return
	}
	for k, e := range w.errors {
		if e.Seen.Before(cutoff) {
			delete(w.errors, k)
		}
	}
}

// beginCycle marks the start of a new compile cycle. Called by the
// parser when it sees a "start of build" boundary sentinel (tsc's
// "Starting compilation", cargo's "Compiling", pyright's "Analyzing:")
// or by the polling supervisor at the top of each iteration.
func (w *BuildWatcher) beginCycle() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cycleStart = time.Now()
}

// ---- command specs -------------------------------------------------

// watchMode distinguishes supervisor semantics.
//
//   - watchModeStreaming: the subprocess is long-lived (tsc --watch,
//     pyright --watch). Clean exit is UNEXPECTED and burns a restart
//     budget slot.
//   - watchModePolling: the subprocess is one-shot (go build, cargo
//     check). Clean exit is the NORMAL cycle terminator; supervise
//     sleeps pollInterval and re-runs without bumping restarts.
//     watchMaxRestarts only catches genuine start failures (binary
//     missing, permission denied).
type watchMode int

const (
	watchModeStreaming watchMode = 0 // default
	watchModePolling   watchMode = 1
)

type watchCommandSpec struct {
	program      string
	args         []string
	mode         watchMode
	pollInterval time.Duration // only used when mode == watchModePolling
}

// watchCommandFor returns the watch-mode invocation for a stack, or nil
// when the stack has no meaningful watcher. The binaries here (tsc,
// cargo, go, pyright) may or may not be on PATH — Start handles
// missing-executable failure by marking the watcher dead and proceeding
// with an empty queue, so a missing binary is never fatal.
func watchCommandFor(stack ExecutorKind) *watchCommandSpec {
	switch stack {
	case ExecTS:
		// --pretty false keeps the output single-line per diagnostic,
		// which is what the tsErrorRe regex expects.
		return &watchCommandSpec{
			program: "tsc",
			args:    []string{"--watch", "--noEmit", "--pretty", "false"},
		}
	case ExecCargo:
		// --message-format short emits `path:line:col: error[code]:
		// message`, matching the rust regex below. cargo-watch is a
		// separate crate we can't assume is installed, so we run
		// plain `cargo check` in polling mode — clean exit is NOT a
		// crash, it's the end of a compile cycle. The supervisor
		// sleeps pollInterval between runs.
		return &watchCommandSpec{
			program:      "cargo",
			args:         []string{"check", "--message-format", "short", "--quiet"},
			mode:         watchModePolling,
			pollInterval: 15 * time.Second,
		}
	case ExecGoMod:
		// `go build ./...` is one-shot; same polling semantics as
		// cargo. Exit 0 = clean cycle, exit non-zero = errors
		// captured by the parser, both normal. Budget only trips on
		// "go binary missing" or similar start failures.
		return &watchCommandSpec{
			program:      "go",
			args:         []string{"build", "./..."},
			mode:         watchModePolling,
			pollInterval: 20 * time.Second,
		}
	case ExecPyright:
		return &watchCommandSpec{
			program: "pyright",
			args:    []string{"--watch", "--outputjson=false"},
		}
	}
	return nil
}

// ---- parsing -------------------------------------------------------

// Package-level regexes. Each matches the canonical single-line error
// format emitted by the tool its comment names.

// tsc --pretty false:
//
//	apps/web/src/x.tsx(14,3): error TS2322: Type 'string' is not assignable to 'number'.
var tsErrorRe = regexp.MustCompile(`^(.+?)\((\d+),(\d+)\):\s+error\s+(TS\d+):\s+(.+)$`)

// tsc watch-mode sentinel that signals end-of-batch.
var tsBoundaryRe = regexp.MustCompile(`(?i)Watching for file changes|Starting compilation in watch mode|Starting incremental compilation|File change detected\. Starting incremental compilation`)

// go build:
//
//	./foo.go:14:3: undefined: bar
//	foo/bar.go:7:1: syntax error: ...
var goErrorRe = regexp.MustCompile(`^(\.?/?[^:\s][^:]*\.go):(\d+):(\d+):\s+(.+)$`)

// cargo check --message-format short:
//
//	src/foo.rs:14:3: error[E0308]: mismatched types
var cargoErrorRe = regexp.MustCompile(`^([^:\s][^:]*\.rs):(\d+):(\d+):\s+error(?:\[([A-Z0-9]+)\])?:\s+(.+)$`)

// cargo "Compiling..." / "Checking..." boundary (start-of-build).
var cargoBoundaryRe = regexp.MustCompile(`^\s*(Compiling|Checking|Finished)\s+`)

// pyright --watch text output:
//
//	./foo.py:14:3 - error: Could not find module "bar"
var pyrightErrorRe = regexp.MustCompile(`^(\.?/?[^:\s][^:]*\.py):(\d+):(\d+)\s+-\s+error:\s+(.+)$`)

// pyright boundary between incremental runs.
var pyrightBoundaryRe = regexp.MustCompile(`(?i)Analyzing:|No configuration file found|pyright\s+\d+`)

// watchParser is a stateless per-line classifier with a tiny accumulator
// for the current batch. Feed returns (boundary, newlyParsed). When
// boundary is true the caller should rotate its error set.
type watchParser struct {
	stack     string
	boundary  int
	startedAt time.Time
}

func newWatchParser(stack string) *watchParser {
	return &watchParser{stack: stack, startedAt: time.Now()}
}

// Feed classifies one stderr/stdout line. The returned slice contains
// zero or one CompileError; boundary is true when the line matches a
// known end-of-batch sentinel for the stack.
func (p *watchParser) Feed(line string) (boundary bool, out []CompileError) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return false, nil
	}
	switch p.stack {
	case string(ExecTS):
		if tsBoundaryRe.MatchString(line) {
			p.boundary++
			return true, nil
		}
		if m := tsErrorRe.FindStringSubmatch(line); m != nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			return false, []CompileError{{
				File:    cleanRel(m[1]),
				Line:    ln,
				Column:  col,
				Code:    m[4],
				Message: strings.TrimSpace(m[5]),
				Stack:   p.stack,
			}}
		}
	case string(ExecGoMod):
		if m := goErrorRe.FindStringSubmatch(line); m != nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			return false, []CompileError{{
				File:    cleanRel(m[1]),
				Line:    ln,
				Column:  col,
				Message: strings.TrimSpace(m[4]),
				Stack:   p.stack,
			}}
		}
	case string(ExecCargo):
		if cargoBoundaryRe.MatchString(line) {
			p.boundary++
			return true, nil
		}
		if m := cargoErrorRe.FindStringSubmatch(line); m != nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			return false, []CompileError{{
				File:    cleanRel(m[1]),
				Line:    ln,
				Column:  col,
				Code:    m[4],
				Message: strings.TrimSpace(m[5]),
				Stack:   p.stack,
			}}
		}
	case string(ExecPyright):
		if pyrightBoundaryRe.MatchString(line) {
			p.boundary++
			return true, nil
		}
		if m := pyrightErrorRe.FindStringSubmatch(line); m != nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			return false, []CompileError{{
				File:    cleanRel(m[1]),
				Line:    ln,
				Column:  col,
				Message: strings.TrimSpace(m[4]),
				Stack:   p.stack,
			}}
		}
	}
	return false, nil
}

// Flush returns any errors the parser is still holding. Current
// parsers are stateless between lines, so this is always empty; it
// exists so future multi-line parsers can drain trailing state.
func (p *watchParser) Flush() []CompileError { return nil }

// boundaryCount is how many boundary signals the parser has seen so far
// this run. Useful for ageOut heuristics; currently informational only.
func (p *watchParser) boundaryCount() int { return p.boundary }

// cleanRel normalises a file path the compiler emitted. Strips a
// leading "./" and collapses redundant separators so dedup keys stay
// stable across equivalent paths.
func cleanRel(p string) string {
	p = strings.TrimPrefix(p, "./")
	p = filepath.ToSlash(filepath.Clean(p))
	return p
}

// ---- subprocess teardown ------------------------------------------

// killWatcherProcessGroup sends SIGTERM to the subprocess's process
// group, waits up to 3s for graceful exit, then SIGKILLs. Mirrors the
// engine/claude.go pattern so watcher teardown follows the same shape
// as the rest of stoke's subprocess management.
func killWatcherProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
