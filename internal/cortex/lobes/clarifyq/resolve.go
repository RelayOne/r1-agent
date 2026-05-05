// Resolve-on-answer hook for ClarifyingQLobe (spec item 25).
//
// When a cortex.user.answered_question event arrives carrying a
// question_id in Custom["question_id"], the Lobe matches that ID
// against its outstanding map (populated by haikuOnce in trigger.go)
// and Publishes a follow-on Note with Resolves=originalNoteID. The
// Workspace Spotlight tracker treats the original Note as resolved
// and lets the next-best unresolved candidate take over.
package clarifyq

import (
	"fmt"
	"log/slog"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// resolveAnsweredQuestion is invoked from the cortex.user.answered_question
// subscriber installed in TASK-24. The handler:
//
//  1. Extracts question_id from ev.Custom.
//  2. Pops the matching note_id from l.outstanding (delete on hit).
//  3. Publishes a resolution Note with Resolves=note_id. The
//     resolution Note itself carries severity=info and a short title
//     so the supervisor injection block reflects "answered" without
//     re-pinging the model.
//
// Unknown question_ids are logged at warn level and dropped — the user
// might have answered the same question twice or the Lobe restarted
// after the question_id was issued; either way there is nothing to
// resolve and re-publishing would create a phantom Note.
func (l *ClarifyingQLobe) resolveAnsweredQuestion(ev *hub.Event) {
	if ev == nil {
		return
	}
	questionID, _ := ev.Custom["question_id"].(string)
	if questionID == "" {
		slog.Warn("clarifying-q: answered_question event missing question_id",
			"event", ev.Type)
		return
	}

	l.mu.Lock()
	noteID, ok := l.outstanding[questionID]
	if ok {
		delete(l.outstanding, questionID)
	}
	l.mu.Unlock()

	if !ok {
		slog.Warn("clarifying-q: unknown question_id in answered event",
			"question_id", questionID, "event", ev.Type)
		return
	}

	if l.ws == nil {
		// Outstanding entry already removed — nothing else to do.
		return
	}

	resolution := cortex.Note{
		LobeID:   l.ID(),
		Severity: cortex.SevInfo,
		Title:    fmt.Sprintf("answered: %s", questionID),
		Body:     "user answered the clarifying question",
		Tags:     []string{"clarify", "resolved"},
		Resolves: noteID,
		Meta: map[string]any{
			metaQuestionID: questionID,
		},
	}
	if err := l.ws.Publish(resolution); err != nil {
		slog.Warn("clarifying-q: publish resolution failed",
			"err", err, "question_id", questionID, "note_id", noteID)
	}
}
