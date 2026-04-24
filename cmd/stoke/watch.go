// Package main — watch.go
//
// `stoke watch` — live operator dashboard for an in-flight SOW run.
//
// Consolidates three sources of progress data into a single terminal
// frame that redraws in place:
//
//   1. <repoRoot>/.stoke/sow-state.json — plan.SOWState, per-session
//      status/attempts/acceptance counters written by the scheduler.
//   2. A streaming log file (auto-detected from /tmp/sentinel-sow-run*.log
//      or passed via --log) tailed for emoji-prefixed event lines:
//      ✓ T... done, → dispatching, ↯ decomposing, ⬆ promoted, 📐 sizer,
//      ⚖ judge, 🧽 hygiene, 🔗 integration, 🧠 meta, acceptance check ...
//   3. pgrep for "stoke sow --repo <root>" — alive/dead + pid.
//
// Output is pure Go stdlib (no third-party deps). ANSI box-drawing +
// color unless --no-color is set. --once renders a single frame and
// exits (pipeable). Default refresh: 5s.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RelayOne/r1/internal/plan"
)

// watchCmd implements `stoke watch` — a polling operator dashboard.
func watchCmd(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	repo := fs.String("repo", ".", "Repository root whose .stoke/sow-state.json to track")
	logPath := fs.String("log", "", "Streaming log file to tail (auto-detects /tmp/sentinel-sow-run*.log if blank)")
	interval := fs.Duration("interval", 5*time.Second, "Refresh cadence")
	noColor := fs.Bool("no-color", false, "Disable ANSI color and use ASCII status markers")
	once := fs.Bool("once", false, "Render a single frame and exit (useful for piping)")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}

	w := &watcher{
		repoRoot: absRepo,
		logPath:  *logPath,
		noColor:  *noColor,
	}

	if *once {
		w.render(os.Stdout)
		return
	}

	// Trap SIGINT / SIGTERM so we can leave the terminal in a clean state.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Hide cursor while live, show on exit.
	if !*noColor {
		fmt.Print("\033[?25l")
		defer fmt.Print("\033[?25h")
	}

	for {
		// Clear + home cursor, then paint.
		if !*noColor {
			fmt.Print("\033[2J\033[H")
		} else {
			fmt.Println(strings.Repeat("\n", 2))
		}
		w.render(os.Stdout)

		select {
		case <-ctx.Done():
			return
		case <-time.After(*interval):
		}
	}
}

// watcher is the per-frame renderer state.
type watcher struct {
	repoRoot string
	logPath  string
	noColor  bool
}

// -- log discovery -----------------------------------------------------------

// sentinelLogGlob is the default pattern used when --log is omitted.
const sentinelLogGlob = "/tmp/sentinel-sow-run*.log"

// resolveLogPath returns the effective log file, auto-detecting the most
// recently modified sentinel log when --log wasn't provided.
func (w *watcher) resolveLogPath() string {
	if w.logPath != "" {
		return w.logPath
	}
	matches, err := filepath.Glob(sentinelLogGlob)
	if err != nil || len(matches) == 0 {
		return ""
	}
	type entry struct {
		path string
		mod  time.Time
	}
	es := make([]entry, 0, len(matches))
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil {
			continue
		}
		es = append(es, entry{m, st.ModTime()})
	}
	if len(es) == 0 {
		return ""
	}
	sort.Slice(es, func(i, j int) bool { return es[i].mod.After(es[j].mod) })
	return es[0].path
}

// -- emoji event regexes -----------------------------------------------------
//
// These mirror the grep-line the operator uses today:
//   (^---|🧽|🔗|⚖|↻|↯|✓ T|✗|✔|→ dispatching|⏱|⚠|📐|⏸|🧠|⬆|
//    acceptance check|skills kept|briefings|session S[0-9]|
//    complete at depth|recursion cap|done \(.*\$)
//
// Kept as separate compiled patterns so we can classify as well as filter.

var (
	// activityRE matches any interesting event line.
	activityRE = regexp.MustCompile(
		`(^---|🧽|🔗|⚖|↻|↯|✓ T|✗ |✔|→ dispatching|⏱|⚠|📐|⏸|🧠|⬆|` +
			`acceptance check|skills kept|briefings|session S[0-9]|` +
			`complete at depth|recursion cap|done \(.*\$)`,
	)

	// doneCostRE extracts the cost USD from "done (Ns, T turns, $X.XXXX)".
	// It's deliberately loose about the preceding prefix so it catches
	// T... done, T...-d1-2 done, session done lines, etc.
	doneCostRE = regexp.MustCompile(`done \([^)]*\$(\d+\.\d+)\)`)

	// dispatchRE counts task dispatches.
	dispatchRE = regexp.MustCompile(`→ dispatching`)

	// promotedRE counts promotion events.
	promotedRE = regexp.MustCompile(`⬆ promoted`)

	// taskDoneRE matches completed task lines: "✓ T12 done (..." .
	taskDoneRE = regexp.MustCompile(`✓ (T[\w\-]+) done`)

	// acCheckRE matches "acceptance check attempt N: K/M passed" and also
	// "acceptance check: K/M passed" used in some variants.
	acCheckRE = regexp.MustCompile(`acceptance check[^:]*:\s*(\d+)/(\d+)\s*passed`)

	// loadedSOWRE matches "loaded SOW: N sessions / M tasks" — the header
	// line printed once near the top of the log. Used as a last-ditch
	// task total when sow-state.json lacks task records.
	loadedSOWRE = regexp.MustCompile(`loaded SOW:\s*(\d+)\s+sessions?\s*/\s*(\d+)\s+tasks?`)

	// sessionCtxRE optionally tags a log line with its owning session ID so
	// we can attribute acceptance checks per session. Matches "session S3"
	// or "[S3]" shapes.
	sessionCtxRE = regexp.MustCompile(`(?:session\s+|\[)(S[\w\-]+)\]?`)
)

// -- frame render ------------------------------------------------------------

// frame aggregates everything we need to paint a single dashboard frame.
type frame struct {
	logPath     string
	elapsed     time.Duration
	alive       bool
	pid         int
	state       *plan.SOWState
	stateErr    error
	sessionsDone, sessionsActive, sessionsQueued, sessionsFailed int
	promoted    int
	tasksDone   int
	tasksInFlight int
	tasksTotal  int
	totalCost   float64
	events      []logEvent
	// perSessionACs: session ID -> "K/M" as last seen in log.
	perSessionACs map[string]string
}

// logEvent is one parsed activity line.
type logEvent struct {
	offset time.Time // best-effort, derived from file mtime bucketing
	line   string
}

// render paints one dashboard frame to w.
func (wc *watcher) render(out *os.File) {
	f := wc.collect()
	wc.paint(out, f)
}

// collect gathers all inputs for one frame.
func (wc *watcher) collect() *frame {
	f := &frame{
		logPath:       wc.resolveLogPath(),
		perSessionACs: map[string]string{},
	}

	// Log file mtime -> elapsed.
	if f.logPath != "" {
		if st, err := os.Stat(f.logPath); err == nil {
			f.elapsed = time.Since(fileCreatedTime(st))
		}
	}

	// State.
	st, err := plan.LoadSOWState(wc.repoRoot)
	f.state, f.stateErr = st, err
	if st != nil {
		// If state file exists but log doesn't, fall back to state mtime.
		if f.logPath == "" {
			if sst, serr := os.Stat(plan.SOWStatePath(wc.repoRoot)); serr == nil {
				f.elapsed = time.Since(st.StartedAt)
				_ = sst
			}
		}
		for _, s := range st.Sessions {
			switch s.Status {
			case "done":
				f.sessionsDone++
			case "running":
				f.sessionsActive++
			case "failed":
				f.sessionsFailed++
			case "pending":
				f.sessionsQueued++
			case "skipped":
				// don't count
			}
			// Sum per-session task records if present.
			f.tasksTotal += len(s.TaskResults)
			for _, t := range s.TaskResults {
				_ = t // counting is done from log; state records are opaque
			}
		}
	}

	// Process detection.
	f.pid, f.alive = detectStokePid(wc.repoRoot)

	// Log-derived metrics.
	if f.logPath != "" {
		tail, err := tailBytes(f.logPath, 256*1024) // last ~256KB
		if err == nil {
			wc.parseTail(tail, f)
		}
	}

	// Compute task totals: prefer sow-state session TaskResults, else
	// loaded-SOW header, else what we scraped from the log.
	if f.tasksTotal == 0 && f.logPath != "" {
		if head, err := headBytes(f.logPath, 64*1024); err == nil {
			if m := loadedSOWRE.FindStringSubmatch(string(head)); len(m) == 3 {
				if n, perr := strconv.Atoi(m[2]); perr == nil {
					f.tasksTotal = n
				}
			}
		}
	}

	// Derive in-flight: dispatches - done, clamped to >=0.
	// (Scraped inside parseTail.)
	if f.tasksInFlight < 0 {
		f.tasksInFlight = 0
	}

	return f
}

// parseTail scans the log tail and populates f.events, f.tasksDone,
// f.tasksInFlight, f.promoted, f.totalCost, f.perSessionACs.
func (wc *watcher) parseTail(tail []byte, f *frame) {
	// Line split.
	lines := strings.Split(string(tail), "\n")
	// The first chunk may be a partial line — drop it to avoid garbled
	// regex hits on the leading byte offset.
	if len(lines) > 1 {
		lines = lines[1:]
	}

	dispatched := 0
	done := 0
	currentSession := ""

	var acts []logEvent
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		// Track ambient session context for AC attribution.
		if sm := sessionCtxRE.FindStringSubmatch(ln); len(sm) == 2 {
			currentSession = sm[1]
		}
		if dispatchRE.MatchString(ln) {
			dispatched++
		}
		if taskDoneRE.MatchString(ln) {
			done++
		}
		if promotedRE.MatchString(ln) {
			f.promoted++
		}
		if m := doneCostRE.FindStringSubmatch(ln); len(m) == 2 {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				f.totalCost += v
			}
		}
		if m := acCheckRE.FindStringSubmatch(ln); len(m) == 3 {
			key := currentSession
			if key == "" {
				key = "_"
			}
			f.perSessionACs[key] = m[1] + "/" + m[2]
		}
		if activityRE.MatchString(ln) {
			acts = append(acts, logEvent{line: strings.TrimRight(ln, "\r\n")})
		}
	}

	f.tasksDone = done
	f.tasksInFlight = dispatched - done
	if f.tasksInFlight < 0 {
		f.tasksInFlight = 0
	}
	// Keep last 10 activity events.
	if len(acts) > 10 {
		acts = acts[len(acts)-10:]
	}
	// Best-effort timestamping: stamp all events with log mtime. We don't
	// have per-line timestamps, so this is approximate — good enough for
	// "was this recent?" eyeballing.
	if f.logPath != "" {
		if st, err := os.Stat(f.logPath); err == nil {
			mt := st.ModTime()
			for i := range acts {
				acts[i].offset = mt
			}
		}
	}
	f.events = acts
}

// -- paint -------------------------------------------------------------------

// color helpers: when noColor is true these return s unchanged.
func (wc *watcher) dim(s string) string   { return wc.col(s, "\033[90m") }
func (wc *watcher) bold(s string) string  { return wc.col(s, "\033[1m") }
func (wc *watcher) green(s string) string { return wc.col(s, "\033[32m") }
func (wc *watcher) yellow(s string) string { return wc.col(s, "\033[33m") }
func (wc *watcher) red(s string) string   { return wc.col(s, "\033[31m") }
func (wc *watcher) cyan(s string) string  { return wc.col(s, "\033[36m") }

func (wc *watcher) col(s, code string) string {
	if wc.noColor {
		return s
	}
	return code + s + "\033[0m"
}

// icon picks a status glyph; falls back to ASCII when noColor.
func (wc *watcher) icon(kind string) string {
	if wc.noColor {
		switch kind {
		case "done":
			return "[OK]"
		case "active":
			return "[..]"
		case "queued":
			return "[--]"
		case "failed":
			return "[XX]"
		case "promoted":
			return "[^^]"
		case "warn":
			return "[!]"
		case "alive":
			return "(*)"
		case "dead":
			return "( )"
		}
		return "[ ]"
	}
	switch kind {
	case "done":
		return wc.green("✔")
	case "active":
		return wc.yellow("◯")
	case "queued":
		return wc.dim("⏳")
	case "failed":
		return wc.red("✗")
	case "promoted":
		return wc.cyan("⬆")
	case "warn":
		return wc.yellow("⚠")
	case "alive":
		return wc.green("●")
	case "dead":
		return wc.red("○")
	}
	return " "
}

// width of the inner box (content area between the "│ " left margin and the
// trailing "│" border).
const innerWidth = 60

// paint draws the frame to out.
func (wc *watcher) paint(out *os.File, f *frame) {
	bw := bufio.NewWriter(out)
	defer bw.Flush()

	top := "╭─ " + wc.bold("stoke watch") + " " + strings.Repeat("─", innerWidth-len(" stoke watch ")-1) + "╮"
	bot := "╰" + strings.Repeat("─", innerWidth+2) + "╯"
	if wc.noColor {
		top = "+-- stoke watch " + strings.Repeat("-", innerWidth-len("-- stoke watch ")) + "+"
		bot = "+" + strings.Repeat("-", innerWidth+2) + "+"
	}

	fmt.Fprintln(bw, top)

	// Header: run path + elapsed.
	runLabel := f.logPath
	if runLabel == "" {
		runLabel = wc.dim("(no log file)")
	}
	elapsed := humanDuration(f.elapsed)
	wc.row(bw, "run", runLabel+wc.dim(" · "+elapsed+" elapsed"))

	// Status.
	statusTxt := wc.icon("dead") + " dead"
	if f.alive {
		statusTxt = wc.icon("alive") + " alive (pid " + strconv.Itoa(f.pid) + ")"
	}
	wc.row(bw, "status", statusTxt)
	wc.blank(bw)

	// State presence / errors.
	if f.stateErr != nil {
		wc.row(bw, "state", wc.red("error: "+f.stateErr.Error()))
	}
	if f.state == nil && f.stateErr == nil {
		wc.row(bw, "state", wc.yellow("no SOW state yet at "+plan.SOWStatePath(wc.repoRoot)))
	}

	// Summary counters.
	if f.state != nil {
		sess := fmt.Sprintf("%d done · %d active · %d queued · %d promoted",
			f.sessionsDone, f.sessionsActive, f.sessionsQueued, f.promoted)
		if f.sessionsFailed > 0 {
			sess += " · " + wc.red(fmt.Sprintf("%d failed", f.sessionsFailed))
		}
		wc.row(bw, "sessions", sess)
	}

	taskTotal := f.tasksTotal
	if taskTotal == 0 {
		taskTotal = f.tasksDone + f.tasksInFlight
	}
	taskLine := fmt.Sprintf("%d/%d complete · %d in flight", f.tasksDone, taskTotal, f.tasksInFlight)
	if taskTotal > f.tasksDone+f.tasksInFlight {
		taskLine += fmt.Sprintf(" · %d queued", taskTotal-f.tasksDone-f.tasksInFlight)
	}
	wc.row(bw, "tasks", taskLine)

	avg := 0.0
	if f.tasksDone > 0 {
		avg = f.totalCost / float64(f.tasksDone)
	}
	wc.row(bw, "cost", fmt.Sprintf("$%.2f (avg $%.2f/task)", f.totalCost, avg))

	// Session breakdown.
	if f.state != nil && len(f.state.Sessions) > 0 {
		wc.blank(bw)
		wc.row(bw, "", wc.bold("session breakdown:"))
		for _, s := range f.state.Sessions {
			wc.renderSession(bw, s, f)
		}
	}

	// Recent activity.
	if len(f.events) > 0 {
		wc.blank(bw)
		wc.row(bw, "", wc.bold(fmt.Sprintf("recent activity (last %d events):", len(f.events))))
		for _, ev := range f.events {
			ts := ev.offset.Format("15:04:05")
			if ev.offset.IsZero() {
				ts = "--:--:--"
			}
			wc.row(bw, "", wc.dim(ts)+"  "+truncate(ev.line, innerWidth-12))
		}
	}

	wc.blank(bw)
	wc.row(bw, "", wc.dim("press ctrl-c to exit"))
	fmt.Fprintln(bw, bot)
}

// renderSession paints one session breakdown row.
func (wc *watcher) renderSession(bw *bufio.Writer, s plan.SessionRecord, f *frame) {
	var icon string
	switch s.Status {
	case "done":
		icon = wc.icon("done")
	case "running":
		icon = wc.icon("active")
	case "failed":
		icon = wc.icon("failed")
	case "pending":
		icon = wc.icon("queued")
	default:
		icon = wc.icon("queued")
	}

	// Acceptance summary: prefer the per-session record, else log scrape.
	var acs string
	if len(s.Acceptance) > 0 {
		passed := 0
		for _, a := range s.Acceptance {
			if a.Passed {
				passed++
			}
		}
		acs = fmt.Sprintf("%d/%d ACs", passed, len(s.Acceptance))
	} else if v, ok := f.perSessionACs[s.SessionID]; ok {
		acs = v + " ACs"
	} else {
		acs = wc.dim("— ACs")
	}

	title := truncate(s.Title, 30)
	extra := ""
	if len(s.TaskResults) > 0 {
		extra = fmt.Sprintf("· %d tasks", len(s.TaskResults))
	}
	if s.Attempts > 0 {
		extra += fmt.Sprintf(" · attempt %d", s.Attempts)
	}
	line := fmt.Sprintf("%s %-5s %-30s %s %s",
		icon, s.SessionID, title, acs, wc.dim(extra))
	wc.row(bw, "", line)
}

// row prints "│ <label>  <value>  │" with padding; label may be empty.
func (wc *watcher) row(bw *bufio.Writer, label, value string) {
	left := "│ "
	right := " │"
	if wc.noColor {
		left, right = "| ", " |"
	}
	var body string
	if label == "" {
		body = "  " + value
	} else {
		body = fmt.Sprintf("%-8s %s", label, value)
	}
	// Pad the visible length (ignoring ANSI) to innerWidth.
	pad := innerWidth - visibleLen(body)
	if pad < 0 {
		pad = 0
	}
	fmt.Fprintln(bw, left+body+strings.Repeat(" ", pad)+right)
}

// blank prints an empty interior row.
func (wc *watcher) blank(bw *bufio.Writer) {
	wc.row(bw, "", "")
}

// -- helpers -----------------------------------------------------------------

// detectStokePid runs pgrep for an in-flight `stoke sow --repo <root>` and
// returns (pid, true) on match, (0, false) otherwise.
func detectStokePid(repoRoot string) (int, bool) {
	// Match the repo argument loosely: pgrep's -f matches the full command
	// line. We drop the trailing slash to be tolerant of normalization.
	needle := "stoke sow.*" + regexp.QuoteMeta(strings.TrimRight(repoRoot, "/"))
	cmd := exec.Command("pgrep", "-f", needle) // #nosec G204 -- Stoke self-invocation or dev-tool binary with Stoke-generated args.
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if pid, perr := strconv.Atoi(ln); perr == nil {
			return pid, true
		}
	}
	return 0, false
}

// tailBytes returns the last n bytes of a file, or the whole file if smaller.
// Non-blocking: opens, seeks, reads, closes.
func tailBytes(path string, n int64) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	start := int64(0)
	if fi.Size() > n {
		start = fi.Size() - n
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil, err
	}
	buf := make([]byte, fi.Size()-start)
	_, err = f.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// headBytes reads up to the first n bytes of a file.
func headBytes(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	k, err := f.Read(buf)
	if err != nil && k == 0 {
		return nil, err
	}
	return buf[:k], nil
}

// fileCreatedTime returns the best-available file creation time. On Linux
// we fall back to ModTime since birth-time isn't reliably exposed via
// syscall without platform-specific code paths.
func fileCreatedTime(st os.FileInfo) time.Time {
	return st.ModTime()
}

// humanDuration renders a duration as "1h12m" / "42s" / "3m5s".
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int(d / time.Minute)
	d -= time.Duration(m) * time.Minute
	s := int(d / time.Second)
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// truncate shortens s to maxRunes runes, appending an ellipsis when cut.
func truncate(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	if maxRunes <= 1 {
		return string(r[:maxRunes])
	}
	return string(r[:maxRunes-1]) + "…"
}

// visibleLen counts rune length ignoring ANSI CSI escape sequences.
func visibleLen(s string) int {
	n := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			continue
		}
		n++
	}
	return n
}
