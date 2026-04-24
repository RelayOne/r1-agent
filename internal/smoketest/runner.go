// Package smoketest — runner.go
//
// Executes the Runtime's commands and produces a Verdict. Runnable
// runtimes run RunCommands and fail on any non-zero exit. Static-only
// runtimes run StaticCommands (or none, when even those can't run)
// and always produce a VerdictStaticOnly — they inform but never
// block a session from recording as done.
//
// No LLM interaction in v1. A later revision can replace
// runCommands with an LLM-written smoke test scoped to the specific
// session, but for the user's #1 goal of "real shippable build with
// no fakes" the deterministic environment-level checks are the
// foundation; the LLM layer is an optimization on top.

package smoketest

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// VerdictKind is the coarse outcome of a smoke run.
type VerdictKind string

const (
	VerdictPass       VerdictKind = "pass"
	VerdictFail       VerdictKind = "fail"
	VerdictStaticOnly VerdictKind = "static_only" // structural checks only; runtime could not execute this class
	VerdictSkipped    VerdictKind = "skipped"     // session-level opt-out (no capability match worth attempting)
)

// Verdict is the smoke runner's output for one session.
type Verdict struct {
	Kind        VerdictKind
	Capability  Capability
	Reason      string   // short human-readable summary
	Commands    []string // commands actually executed (in order)
	FirstFailed string   // command that failed, empty when Pass / StaticOnly
	Output      string   // combined stdout+stderr trimmed for logs
}

// Timeout per smoke command. Smoke is supposed to be fast; if a
// command takes longer than this, something is wrong (hanging test,
// stuck install) and we'd rather fail the smoke than block the
// session forever.
const perCommandTimeout = 5 * time.Minute

// Run executes the appropriate smoke commands for the session and
// returns a Verdict. repoRoot is the working directory for every
// command. pathExtra, when non-empty, is prepended to PATH so e.g.
// node_modules/.bin gets resolved without the smoke worker having
// to cd around.
func Run(ctx context.Context, session plan.Session, repoRoot string) Verdict {
	rt := DetectCapability(session, repoRoot)
	v := Verdict{Capability: rt.Capability, Commands: []string{}}

	cmds := rt.RunCommands
	if !rt.Runnable {
		cmds = rt.StaticCommands
	}
	if len(cmds) == 0 {
		v.Kind = VerdictStaticOnly
		if rt.Reason != "" {
			v.Reason = rt.Reason
		} else {
			v.Reason = "no smoke commands for this capability; session not runtime-verified"
		}
		return v
	}

	var outputBuf bytes.Buffer
	for _, cmd := range cmds {
		v.Commands = append(v.Commands, cmd)
		cctx, cancel := context.WithTimeout(ctx, perCommandTimeout)
		c := exec.CommandContext(cctx, "bash", "-lc", cmd) // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
		c.Dir = repoRoot
		out, err := c.CombinedOutput()
		cancel()
		outputBuf.WriteString("$ " + cmd + "\n")
		outputBuf.Write(out)
		outputBuf.WriteString("\n")
		if err != nil {
			v.Kind = VerdictFail
			v.FirstFailed = cmd
			v.Output = tail(outputBuf.String(), 8000)
			if rt.Runnable {
				v.Reason = fmt.Sprintf("%s smoke failed at: %s", rt.Capability, truncateLine(cmd, 80))
			} else {
				// Static check failed — treat as Fail not StaticOnly
				// because structural guarantees must hold even when we
				// can't run the feature.
				v.Reason = fmt.Sprintf("%s static check failed at: %s", rt.Capability, truncateLine(cmd, 80))
			}
			return v
		}
	}

	v.Output = tail(outputBuf.String(), 8000)
	if rt.Runnable {
		v.Kind = VerdictPass
		v.Reason = fmt.Sprintf("%s smoke: %d command(s) passed", rt.Capability, len(cmds))
	} else {
		v.Kind = VerdictStaticOnly
		v.Reason = fmt.Sprintf("%s: static checks passed (%d command(s)); %s",
			rt.Capability, len(cmds), rt.Reason)
	}
	return v
}

// FormatVerdict renders a Verdict for the post-session banner. One
// line per verdict; includes the capability so the operator sees
// *why* a session was static-only.
func FormatVerdict(sessionID string, v Verdict) string {
	var icon string
	switch v.Kind {
	case VerdictPass:
		icon = "✔"
	case VerdictFail:
		icon = "⛔"
	case VerdictStaticOnly:
		icon = "◉"
	case VerdictSkipped:
		icon = "—"
	}
	return fmt.Sprintf("%s smoke %s [%s]: %s", icon, sessionID, v.Capability, v.Reason)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "... (truncated)\n" + s[len(s)-n:]
}

func truncateLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
