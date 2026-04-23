package main

// ctl_cmd.go — CDC-13 + CDC-14: operator-control CLI wrappers.
//
// Eight verbs sharing one dispatcher, routed from main.go:
//
//	stoke status   [<session_id>] [--json] [--ctl-dir DIR] [--ctl-url URL]
//	stoke approve  <session_id> [--approval-id ID] [--decision yes|no] [--reason STR]
//	stoke override <session_id> <ac_id> [--reason STR]
//	stoke budget   <session_id> --add USD [--dry-run]
//	stoke pause    <session_id>
//	stoke resume   <session_id>
//	stoke inject   <session_id> "text..."  [--priority N]
//	stoke takeover <session_id> [--reason STR] [--max-duration 10m]
//
// Each verb marshals a payload, calls sessionctl.Call against the target
// socket, and renders the response. Exit code 0 on OK=true, 1 on OK=false,
// 2 on usage errors.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ericmacdougall/stoke/internal/sessionctl"
)

// runCtlCmd dispatches the top-level verb to the per-verb runner.
func runCtlCmd(verb string, args []string, stdout, stderr io.Writer) int {
	switch verb {
	case "status":
		return runStatusCmd(args, stdout, stderr)
	case "approve":
		return runApproveCmd(args, stdout, stderr)
	case "override":
		return runOverrideCmd(args, stdout, stderr)
	case "budget":
		return runBudgetCmd(args, stdout, stderr)
	case "pause":
		return runPauseCmd(args, stdout, stderr)
	case "resume":
		return runResumeCmd(args, stdout, stderr)
	case "inject":
		return runInjectCmd(args, stdout, stderr)
	case "takeover":
		return runTakeoverCmd(args, stdout, stderr)
	}
	fmt.Fprintln(stderr, "unknown verb:", verb)
	return 2
}

// sessionSocketPath returns <ctlDir>/stoke-<sessionID>.sock, defaulting
// ctlDir to /tmp.
func sessionSocketPath(ctlDir, sessionID string) string {
	if ctlDir == "" {
		ctlDir = "/tmp"
	}
	return filepath.Join(ctlDir, "stoke-"+sessionID+".sock")
}

// ulid is a cheap request-id generator (crypto/rand + hex, 22 chars). Not
// a real ULID — the sessionctl protocol only needs uniqueness per caller.
func ulid() string {
	var buf [11]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Extremely unlikely; fall back to a timestamp-based id so the
		// request still has something unique to the call.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// splitPositionalsAndFlags partitions args into (positionals, flagArgs)
// so flags can appear in any position relative to positional arguments
// on the CLI. A flag is any arg that starts with "-"; the following
// token is consumed as its value unless the flag is bool-shaped (has
// "=" embedded, e.g. "--dry-run" with no value) -- the caller supplies
// the FlagSet so bool lookup knows which flags swallow a value.
//
// Semantics match Go's stdlib flag package rules: "--" terminates flag
// scanning and routes the remainder to positionals.
func splitPositionalsAndFlags(fs *flag.FlagSet, args []string) (positionals, flagArgs []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			return
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positionals = append(positionals, a)
			continue
		}
		flagArgs = append(flagArgs, a)
		// Determine if this flag takes a separate value (no "=" in it
		// AND the next arg is the value). Booleans consume no value.
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			// --flag=val -- value is attached, don't consume next.
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			// Unknown flag; leave it to fs.Parse to report the error.
			continue
		}
		if isBoolFlag(f) {
			continue
		}
		if i+1 < len(args) {
			flagArgs = append(flagArgs, args[i+1])
			i++
		}
	}
	return
}

// isBoolFlag returns true if f.Value is a boolFlag (Go's flag.Value
// interface extension for bool-like flags that don't need a value).
func isBoolFlag(f *flag.Flag) bool {
	type boolFlag interface {
		IsBoolFlag() bool
	}
	if bf, ok := f.Value.(boolFlag); ok {
		return bf.IsBoolFlag()
	}
	return false
}

// callVerb marshals payload to JSON, dispatches a sessionctl.Request to
// the given socket, and returns the decoded Response.
func callVerb(sock, verb string, payload any) (sessionctl.Response, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return sessionctl.Response{}, fmt.Errorf("marshal payload: %w", err)
		}
		raw = b
	}
	return sessionctl.Call(sock, sessionctl.Request{
		Verb:      verb,
		RequestID: ulid(),
		Payload:   raw,
	})
}

// ---- status ----------------------------------------------------------------

// runStatusCmd implements `stoke status [...]`.
//
// Two modes:
//
//	no positional arg → discover sockets in --ctl-dir and render a table
//	<session_id>      → query just that session
//
// --json dumps the raw sessionctl Response.Data for every queried
// session as a JSON array (discovery mode) or single object (scoped
// mode). --ctl-url is accepted for forward-compat but is not yet wired.
func runStatusCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ctlDir := fs.String("ctl-dir", "/tmp", "directory holding stoke-*.sock files")
	ctlURL := fs.String("ctl-url", "", "sessionctl HTTP endpoint (not yet supported)")
	asJSON := fs.Bool("json", false, "emit raw JSON instead of table")
	positionals, flagArgs := splitPositionalsAndFlags(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if *ctlURL != "" {
		fmt.Fprintln(stderr, "status: --ctl-url is not yet supported")
		return 2
	}
	rest := positionals

	// Single-session mode.
	if len(rest) >= 1 {
		sid := rest[0]
		sock := sessionSocketPath(*ctlDir, sid)
		resp, err := callVerb(sock, sessionctl.VerbStatus, nil)
		if err != nil {
			fmt.Fprintf(stderr, "status: %v\n", err)
			return 1
		}
		if !resp.OK {
			fmt.Fprintf(stderr, "status: %s\n", resp.Error)
			return 1
		}
		if *asJSON {
			return emitStatusJSON(stdout, stderr, map[string]json.RawMessage{sid: resp.Data})
		}
		return renderStatusDetail(stdout, stderr, sid, resp.Data)
	}

	// Discovery mode.
	socks, err := sessionctl.DiscoverSessions(*ctlDir)
	if err != nil {
		fmt.Fprintf(stderr, "status: discover: %v\n", err)
		return 1
	}
	if len(socks) == 0 {
		fmt.Fprintln(stdout, "no running sessions")
		return 0
	}
	sort.Strings(socks)

	collected := make(map[string]json.RawMessage, len(socks))
	// Preserve deterministic ordering for the table/JSON output.
	orderedIDs := make([]string, 0, len(socks))
	for _, sock := range socks {
		sid := sessionctl.SessionIDFromSocket(sock)
		if sid == "" {
			continue
		}
		resp, err := callVerb(sock, sessionctl.VerbStatus, nil)
		if err != nil {
			// Dial failure or pruning — warn and move to the next socket.
			fmt.Fprintf(stderr, "pruning stale socket %s\n", sock)
			sessionctl.PruneStaleSocket(sock)
			continue
		}
		if !resp.OK {
			fmt.Fprintf(stderr, "status: %s: %s\n", sid, resp.Error)
			continue
		}
		collected[sid] = resp.Data
		orderedIDs = append(orderedIDs, sid)
	}

	if len(collected) == 0 {
		fmt.Fprintln(stdout, "no running sessions")
		return 0
	}

	if *asJSON {
		return emitStatusJSONOrdered(stdout, stderr, collected, orderedIDs)
	}
	return renderStatusTable(stdout, stderr, collected, orderedIDs)
}

// emitStatusJSON writes a JSON array of {session_id, data} objects, one
// per session. Used when the caller queried a single session by id; we
// still wrap it in an array for shape consistency with discovery mode.
func emitStatusJSON(stdout, stderr io.Writer, byID map[string]json.RawMessage) int {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return emitStatusJSONOrdered(stdout, stderr, byID, ids)
}

// emitStatusJSONOrdered writes the JSON array in the given ID order.
func emitStatusJSONOrdered(stdout, stderr io.Writer, byID map[string]json.RawMessage, order []string) int {
	out := make([]map[string]any, 0, len(order))
	for _, id := range order {
		entry := map[string]any{"session_id": id}
		data := byID[id]
		if len(data) > 0 {
			var parsed any
			if err := json.Unmarshal(data, &parsed); err == nil {
				entry["status"] = parsed
			} else {
				entry["status_raw"] = string(data)
			}
		}
		out = append(out, entry)
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(stderr, "status: encode: %v\n", err)
		return 1
	}
	return 0
}

// renderStatusTable prints the discovery-mode columnar view.
func renderStatusTable(stdout, stderr io.Writer, byID map[string]json.RawMessage, order []string) int {
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tMODE\tSTATE\tCOST\tBUDGET\tPAUSED")
	for _, id := range order {
		snap := decodeStatus(byID[id])
		fmt.Fprintf(tw, "%s\t%s\t%s\t$%.2f\t$%.2f\t%s\n",
			id,
			orDash(snap.Mode),
			orDash(snap.State),
			snap.CostUSD,
			snap.BudgetUSD,
			boolYesNo(snap.Paused),
		)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stderr, "status: tabwriter flush: %v\n", err)
		return 1
	}
	return 0
}

// renderStatusDetail prints the single-session expanded view.
func renderStatusDetail(stdout, stderr io.Writer, sid string, data json.RawMessage) int {
	snap := decodeStatus(data)
	fmt.Fprintf(stdout, "session: %s\n", sid)
	fmt.Fprintf(stdout, "  mode:    %s\n", orDash(snap.Mode))
	fmt.Fprintf(stdout, "  state:   %s\n", orDash(snap.State))
	fmt.Fprintf(stdout, "  cost:    $%.2f\n", snap.CostUSD)
	fmt.Fprintf(stdout, "  budget:  $%.2f\n", snap.BudgetUSD)
	fmt.Fprintf(stdout, "  paused:  %s\n", boolYesNo(snap.Paused))
	if snap.PlanID != "" {
		fmt.Fprintf(stdout, "  plan:    %s\n", snap.PlanID)
	}
	if snap.Task != nil {
		fmt.Fprintf(stdout, "  task:    %s (%s) [%s]\n",
			snap.Task.ID, snap.Task.Title, snap.Task.Phase)
	}
	return 0
}

// decodeStatus best-effort unmarshals a StatusSnapshot, returning a zero
// value on parse failure so rendering still produces a row.
func decodeStatus(data json.RawMessage) sessionctl.StatusSnapshot {
	var snap sessionctl.StatusSnapshot
	if len(data) == 0 {
		return snap
	}
	_ = json.Unmarshal(data, &snap)
	return snap
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func boolYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// ---- approve ---------------------------------------------------------------

func runApproveCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ctlDir := fs.String("ctl-dir", "/tmp", "directory holding the socket")
	approvalID := fs.String("approval-id", "", "specific approval id; omit to auto-pick oldest")
	decision := fs.String("decision", "yes", "yes|no")
	reason := fs.String("reason", "", "free-text reason (optional)")
	positionals, flagArgs := splitPositionalsAndFlags(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(positionals) < 1 {
		fmt.Fprintln(stderr, "usage: stoke approve <session_id> [--approval-id ID] [--decision yes|no] [--reason STR]")
		return 2
	}
	sid := positionals[0]

	sock := sessionSocketPath(*ctlDir, sid)
	resp, err := callVerb(sock, sessionctl.VerbApprove, map[string]any{
		"approval_id": *approvalID,
		"decision":    *decision,
		"reason":      *reason,
	})
	if err != nil {
		fmt.Fprintf(stderr, "approve: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(stderr, "approve: %s\n", resp.Error)
		return 1
	}

	// Pull the matched_ask_id back out of Data for the confirmation line.
	matched := pickString(resp.Data, "matched_ask_id")
	fmt.Fprintf(stdout, "approved: ask=%s event=%s\n", orDash(matched), orDash(resp.EventID))
	return 0
}

// ---- override --------------------------------------------------------------

func runOverrideCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("override", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ctlDir := fs.String("ctl-dir", "/tmp", "directory holding the socket")
	reason := fs.String("reason", "", "free-text reason (required by handler)")
	positionals, flagArgs := splitPositionalsAndFlags(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(positionals) < 2 {
		fmt.Fprintln(stderr, "usage: stoke override <session_id> <ac_id> [--reason STR]")
		return 2
	}
	sid := positionals[0]
	acID := positionals[1]

	sock := sessionSocketPath(*ctlDir, sid)
	resp, err := callVerb(sock, sessionctl.VerbOverride, map[string]any{
		"ac_id":  acID,
		"reason": *reason,
	})
	if err != nil {
		fmt.Fprintf(stderr, "override: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(stderr, "override: %s\n", resp.Error)
		return 1
	}
	fmt.Fprintf(stdout, "override: ac=%s event=%s\n", acID, orDash(resp.EventID))
	return 0
}

// ---- budget ----------------------------------------------------------------

func runBudgetCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("budget", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ctlDir := fs.String("ctl-dir", "/tmp", "directory holding the socket")
	add := fs.Float64("add", 0, "amount in USD to add to the budget (required)")
	dryRun := fs.Bool("dry-run", false, "show what would change without mutating")
	positionals, flagArgs := splitPositionalsAndFlags(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	// Track whether --add was explicitly set so we can require it.
	addSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "add" {
			addSet = true
		}
	})
	if len(positionals) < 1 {
		fmt.Fprintln(stderr, "usage: stoke budget <session_id> --add USD [--dry-run]")
		return 2
	}
	if !addSet {
		fmt.Fprintln(stderr, "budget: --add is required")
		return 2
	}
	sid := positionals[0]

	sock := sessionSocketPath(*ctlDir, sid)
	resp, err := callVerb(sock, sessionctl.VerbBudgetAdd, map[string]any{
		"delta_usd": *add,
		"dry_run":   *dryRun,
	})
	if err != nil {
		fmt.Fprintf(stderr, "budget: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(stderr, "budget: %s\n", resp.Error)
		return 1
	}
	prev, next := pickFloat(resp.Data, "prev_budget"), pickFloat(resp.Data, "new_budget")
	tag := "applied"
	if *dryRun {
		tag = "dry-run"
	}
	fmt.Fprintf(stdout, "budget %s: $%.2f -> $%.2f (event=%s)\n",
		tag, prev, next, orDash(resp.EventID))
	return 0
}

// ---- pause / resume --------------------------------------------------------

func runPauseCmd(args []string, stdout, stderr io.Writer) int {
	return runPauseResume(args, stdout, stderr, sessionctl.VerbPause, "paused", "paused_at")
}

func runResumeCmd(args []string, stdout, stderr io.Writer) int {
	return runPauseResume(args, stdout, stderr, sessionctl.VerbResume, "resumed", "resumed_at")
}

func runPauseResume(args []string, stdout, stderr io.Writer, verb, tag, tsField string) int {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	ctlDir := fs.String("ctl-dir", "/tmp", "directory holding the socket")
	positionals, flagArgs := splitPositionalsAndFlags(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(positionals) < 1 {
		fmt.Fprintf(stderr, "usage: stoke %s <session_id>\n", verb)
		return 2
	}
	sid := positionals[0]

	sock := sessionSocketPath(*ctlDir, sid)
	resp, err := callVerb(sock, verb, map[string]any{})
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", verb, err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(stderr, "%s: %s\n", verb, resp.Error)
		return 1
	}
	ts := pickString(resp.Data, tsField)
	fmt.Fprintf(stdout, "%s at %s\n", tag, orDash(ts))
	return 0
}

// ---- inject ----------------------------------------------------------------

func runInjectCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("inject", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ctlDir := fs.String("ctl-dir", "/tmp", "directory holding the socket")
	priority := fs.Int("priority", 0, "task priority (default 0)")
	positionals, flagArgs := splitPositionalsAndFlags(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(positionals) < 2 {
		fmt.Fprintln(stderr, "usage: stoke inject <session_id> \"text of the new requirement\" [--priority N]")
		return 2
	}
	sid := positionals[0]
	text := strings.Join(positionals[1:], " ")

	sock := sessionSocketPath(*ctlDir, sid)
	resp, err := callVerb(sock, sessionctl.VerbInject, map[string]any{
		"text":     text,
		"priority": *priority,
	})
	if err != nil {
		fmt.Fprintf(stderr, "inject: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(stderr, "inject: %s\n", resp.Error)
		return 1
	}
	taskID := pickString(resp.Data, "task_id")
	fmt.Fprintf(stdout, "injected: task=%s event=%s\n", orDash(taskID), orDash(resp.EventID))
	return 0
}

// ---- takeover --------------------------------------------------------------

// runTakeoverCmd issues the takeover_request verb. Full interactive PTY
// attachment is the responsibility of CDC-10; this wrapper exercises the
// round-trip so clients receive a deterministic error from the handler
// rather than "unknown verb" until the handler is upgraded.
func runTakeoverCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("takeover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ctlDir := fs.String("ctl-dir", "/tmp", "directory holding the socket")
	reason := fs.String("reason", "", "free-text reason (optional)")
	maxDuration := fs.String("max-duration", "10m", "upper bound on operator hold time")
	positionals, flagArgs := splitPositionalsAndFlags(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(positionals) < 1 {
		fmt.Fprintln(stderr, "usage: stoke takeover <session_id> [--reason STR] [--max-duration 10m]")
		return 2
	}
	sid := positionals[0]

	sock := sessionSocketPath(*ctlDir, sid)
	resp, err := callVerb(sock, sessionctl.VerbTakeoverRequest, map[string]any{
		"reason":       *reason,
		"max_duration": *maxDuration,
	})
	if err != nil {
		fmt.Fprintf(stderr, "takeover: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(stderr, "takeover: %s\n", resp.Error)
		return 1
	}
	fmt.Fprintf(stdout, "takeover: session=%s event=%s\n", sid, orDash(resp.EventID))
	return 0
}

// ---- small helpers ---------------------------------------------------------

// pickString pulls a string value from a sessionctl.Response.Data blob,
// returning "" when missing or not a string. Used for confirmation
// rendering; never load-bearing for exit code logic.
func pickString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// pickFloat pulls a float64 from a Response.Data blob. 0.0 when missing.
func pickFloat(raw json.RawMessage, key string) float64 {
	if len(raw) == 0 {
		return 0
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0
	}
	if v, ok := m[key]; ok {
		switch x := v.(type) {
		case float64:
			return x
		case int:
			return float64(x)
		}
	}
	return 0
}

