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
	"log/slog"

	"github.com/RelayOne/r1/internal/hub"
)

// resolveAnsweredQuestion is invoked from the cortex.user.answered_question
// subscriber. The handler is intentionally simple: pop the question_id
// from the outstanding map and Publish a resolution Note. Unknown
// question_ids are logged at warn level and dropped.
//
// TASK-24 commits this as a stub (early-return) so the package compiles
// with the subscriber registered; TASK-25 fills in the body.
func (l *ClarifyingQLobe) resolveAnsweredQuestion(ev *hub.Event) {
	// TASK-25 wires the body. The early return here keeps TASK-24's
	// commit independently buildable: trigger.go registers the
	// answered-question subscriber but the resolve path is a no-op
	// until TASK-25 lands.
	if ev == nil {
		return
	}
	_ = slog.Default()
}
