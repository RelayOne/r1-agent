package cortex

// OnUserInputMidTurn — the REPL-owned mid-turn input hook from
// specs/cortex-core.md §"OnUserInputMidTurn". The chat REPL calls this
// on every stdin/WS line that arrives while a turn is in flight; the
// Router (Haiku 4.5, 4 tools) decides interrupt / steer / queue_mission
// / just_chat and this hook enacts the cortex-side half of each
// decision. The caller-side halves (starting a fresh turn after an
// interrupt, enqueueing a mission) stay with the REPL — mission
// enqueue is explicitly out of cortex-core scope per the spec's
// §"Out of scope".

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"
)

// routerLobeID is the LobeID stamped on Notes published on behalf of
// the mid-turn Router (steer notes, just_chat replies). The Router is
// not a Lobe, but Notes require a non-empty publisher identity and
// "router" keeps the provenance obvious in lanes/TUI output.
const routerLobeID = "router"

// noteTitleMaxRunes mirrors Note.Validate's 80-rune Title cap. Steer
// titles and just_chat replies come from the model, which is asked for
// ≤80 chars but not guaranteed to comply — clamp instead of dropping
// the Note.
const noteTitleMaxRunes = 80

// OnUserInputMidTurn routes user input that arrived while a turn is
// in-flight (specs/cortex-core.md §"OnUserInputMidTurn"). It calls
// Router.Route with the input plus a Workspace snapshot, then enacts
// the chosen decision:
//
//   - DecisionInterrupt: turnCancel is invoked exactly once (when
//     non-nil) so the caller's per-turn context unwinds; the payload's
//     Source defaults to "user" so the synthetic interrupt message the
//     drop-partial protocol builds is attributed correctly.
//   - DecisionSteer: a Note is published into the Workspace (severity
//     mapped from the payload, default SevAdvice — "critical" is
//     reserved for system Lobes) so the main agent reads it at the
//     next MidturnCheckFn boundary.
//   - DecisionQueueMission: no cortex-side effect. The caller forwards
//     Queue.Brief/Queue.Priority to its own mission queue.
//   - DecisionJustChat: a SevInfo Note carrying the reply is published
//     so UI lanes surface it; the main agent is unaffected.
//
// The decision is returned (by value, matching the spec signature) so
// the caller can render UI feedback and perform the caller-side
// halves. On a Route error the zero RouterDecision is returned and no
// decision is enacted. Publish failures on the steer/just_chat paths
// are logged at WARN but do not fail the call — the routing decision
// already happened and the caller still needs it.
//
// Works before Start: Route and Workspace.Publish have no dependency
// on the cortex lifecycle context, so a router-only Cortex (no Lobes,
// never Started) can serve a chat REPL without spawning goroutines.
func (c *Cortex) OnUserInputMidTurn(ctx context.Context, userInput string, turnCancel context.CancelFunc) (RouterDecision, error) {
	dec, err := c.router.Route(ctx, RouterInput{
		UserInput: userInput,
		Workspace: c.workspace.Snapshot(),
	})
	if err != nil {
		return RouterDecision{}, err
	}

	switch dec.Kind {
	case DecisionInterrupt:
		if dec.Interrupt != nil && dec.Interrupt.Source == "" {
			// Router fills Reason/NewDirection; provenance is ours to
			// stamp — this input came from the human operator.
			dec.Interrupt.Source = "user"
		}
		if turnCancel != nil {
			turnCancel()
		}

	case DecisionSteer:
		if dec.Steer != nil {
			note := Note{
				LobeID:   routerLobeID,
				Severity: steerSeverity(dec.Steer.Severity),
				Title:    clampNoteTitle(dec.Steer.Title, "mid-turn steer"),
				Body:     dec.Steer.Body,
				Tags:     []string{"midturn", "steer"},
			}
			if pubErr := c.workspace.Publish(note); pubErr != nil {
				slog.Warn("cortex/OnUserInputMidTurn: steer note publish failed",
					"component", "cortex",
					"err", pubErr,
				)
			}
		}

	case DecisionQueueMission:
		// Mission enqueue is the caller's job (spec §"Out of scope"):
		// the decision payload carries Brief/Priority and the REPL
		// owns the queue. Nothing to enact here.

	case DecisionJustChat:
		if dec.JustChat != nil && strings.TrimSpace(dec.JustChat.Reply) != "" {
			note := Note{
				LobeID:   routerLobeID,
				Severity: SevInfo,
				Title:    clampNoteTitle(replyFirstLine(dec.JustChat.Reply), "router reply"),
				Body:     dec.JustChat.Reply,
				Tags:     []string{"midturn", "just-chat"},
			}
			if pubErr := c.workspace.Publish(note); pubErr != nil {
				slog.Warn("cortex/OnUserInputMidTurn: just_chat note publish failed",
					"component", "cortex",
					"err", pubErr,
				)
			}
		}
	}

	return *dec, nil
}

// steerSeverity maps the Router's steer-tool severity enum
// (info|advice|warning) onto the Note Severity type. Unknown values —
// including "critical", which the tool schema reserves for system
// Lobes — degrade to SevAdvice, the spec's default steer severity.
func steerSeverity(s string) Severity {
	switch s {
	case string(SevInfo):
		return SevInfo
	case string(SevWarning):
		return SevWarning
	default:
		return SevAdvice
	}
}

// clampNoteTitle returns title truncated to noteTitleMaxRunes runes,
// or fallback when title is blank — Note.Validate rejects both empty
// and >80-rune Titles and a misbehaving Router model must not cost the
// operator the Note.
func clampNoteTitle(title, fallback string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return fallback
	}
	if utf8.RuneCountInString(title) <= noteTitleMaxRunes {
		return title
	}
	runes := []rune(title)
	return string(runes[:noteTitleMaxRunes-1]) + "…"
}

// replyFirstLine returns s up to (not including) the first newline,
// so a multi-line just_chat reply yields a single-line Note Title
// while the full reply stays in Body. (Named to avoid colliding with
// the firstLine diagnostic helper in cortex_test.go.)
func replyFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
