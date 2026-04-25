// cron_tools.go — cron_create, cron_list, cron_delete tool handlers.
//
// T-R1P-006: CronCreate/Delete/List tools for scheduling agent tasks.
//
// These tools manage the current user's crontab via the system `crontab`
// command. Each R1-managed cron entry is tagged with a comment marker
// "# r1-cron:<id>" so they can be identified and removed cleanly without
// disturbing other crontab entries.
//
// All three tools degrade gracefully on systems where crontab is absent
// (returns an informational string, not a Go error).
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// r1CronMarker is the comment prefix that marks R1-managed cron entries.
// Format: "# r1-cron:<id>" on the line BEFORE the cron expression.
const r1CronMarker = "# r1-cron:"

// handleCronCreate adds a new cron job to the current user's crontab.
// The cron expression follows standard 5-field format: min hour dom month dow.
func (r *Registry) handleCronCreate(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		ID         string `json:"id"`         // unique identifier for this cron entry
		Schedule   string `json:"schedule"`   // cron expression, e.g. "0 9 * * 1"
		Command    string `json:"command"`    // shell command to execute
		WorkingDir string `json:"working_dir"` // optional: cd to this dir before running
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.ID) == "" {
		return "", fmt.Errorf("id is required")
	}
	if strings.TrimSpace(args.Schedule) == "" {
		return "", fmt.Errorf("schedule is required")
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	// Reject IDs with newlines or colons (they'd corrupt the marker format).
	if strings.ContainsAny(args.ID, "\n\r:") {
		return "", fmt.Errorf("id must not contain newlines or colons")
	}

	// Read current crontab.
	current, err := readCrontab(ctx)
	if err != nil {
		return fmt.Sprintf("cron_create: %v", err), nil
	}

	// Remove any existing entry with the same ID (idempotent upsert).
	current = removeCronEntry(current, args.ID)

	// Build the new entry.
	cmd := args.Command
	if dir := strings.TrimSpace(args.WorkingDir); dir != "" {
		cmd = fmt.Sprintf("cd %s && %s", shellQuote(dir), cmd)
	}
	entry := fmt.Sprintf("%s%s\n%s %s\n", r1CronMarker, args.ID, args.Schedule, cmd)
	current = strings.TrimRight(current, "\n") + "\n" + entry

	if err := writeCrontab(ctx, current); err != nil {
		return fmt.Sprintf("cron_create: %v", err), nil
	}
	return fmt.Sprintf("cron_create: scheduled %q as %q", args.ID, args.Schedule), nil
}

// handleCronList lists all R1-managed cron entries in the current user's crontab.
func (r *Registry) handleCronList(ctx context.Context, _ json.RawMessage) (string, error) {
	current, err := readCrontab(ctx)
	if err != nil {
		return fmt.Sprintf("cron_list: %v", err), nil
	}

	lines := strings.Split(current, "\n")
	type entry struct {
		id       string
		schedule string
		cmd      string
	}
	var entries []entry
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if !strings.HasPrefix(l, r1CronMarker) {
			continue
		}
		id := strings.TrimPrefix(l, r1CronMarker)
		cronLine := ""
		if i+1 < len(lines) {
			cronLine = lines[i+1]
			i++ // consume the cron expression line
		}
		// Split cron line: first 5 fields = schedule, rest = command.
		fields := strings.Fields(cronLine)
		sched, cmd := "", cronLine
		if len(fields) >= 6 {
			sched = strings.Join(fields[:5], " ")
			cmd = strings.Join(fields[5:], " ")
		}
		entries = append(entries, entry{id: id, schedule: sched, cmd: cmd})
	}

	if len(entries) == 0 {
		return "cron_list: no R1-managed cron entries found", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "R1-managed cron entries (%d):\n\n", len(entries))
	for _, e := range entries {
		fmt.Fprintf(&sb, "  id: %s\n  schedule: %s\n  command: %s\n\n", e.id, e.schedule, e.cmd)
	}
	return sb.String(), nil
}

// handleCronDelete removes an R1-managed cron entry by ID.
func (r *Registry) handleCronDelete(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.ID) == "" {
		return "", fmt.Errorf("id is required")
	}

	current, err := readCrontab(ctx)
	if err != nil {
		return fmt.Sprintf("cron_delete: %v", err), nil
	}

	before := current
	current = removeCronEntry(current, args.ID)
	if current == before {
		return fmt.Sprintf("cron_delete: no entry with id %q found", args.ID), nil
	}

	if err := writeCrontab(ctx, current); err != nil {
		return fmt.Sprintf("cron_delete: %v", err), nil
	}
	return fmt.Sprintf("cron_delete: removed entry %q", args.ID), nil
}

// --- crontab helpers ---

// readCrontab returns the current user's crontab content.
// Returns empty string + informational message when crontab is absent.
func readCrontab(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("crontab"); err != nil {
		return "", fmt.Errorf("crontab not available on this system")
	}
	out, err := exec.CommandContext(ctx, "crontab", "-l").Output() // #nosec G204 -- crontab is a system binary; no user input in args
	if err != nil {
		// Exit code 1 with "no crontab" message = empty crontab (not an error).
		if strings.Contains(err.Error(), "1") {
			return "", nil
		}
		return "", fmt.Errorf("crontab -l: %w", err)
	}
	return string(out), nil
}

// writeCrontab installs content as the current user's crontab.
func writeCrontab(ctx context.Context, content string) error {
	if _, err := exec.LookPath("crontab"); err != nil {
		return fmt.Errorf("crontab not available on this system")
	}
	cmd := exec.CommandContext(ctx, "crontab", "-") // #nosec G204 -- crontab is a system binary
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab install: %w: %s", err, string(out))
	}
	return nil
}

// removeCronEntry removes the marker line and the following cron expression
// line for entries with the given ID from a crontab string.
func removeCronEntry(content, id string) string {
	marker := r1CronMarker + id
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	skip := false
	for _, l := range lines {
		if l == marker {
			skip = true // skip this marker line; next iteration skips the cron line
			continue
		}
		if skip {
			skip = false
			continue // skip the cron expression line following the marker
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// shellQuote wraps s in single quotes for safe shell embedding.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
