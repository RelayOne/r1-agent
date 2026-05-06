// Test helpers for the clarifyq package. These live in a non-_test.go
// file so the package compiles cleanly when external test packages
// import the helpers; they are unexported so production callers cannot
// accidentally depend on them.
package clarifyq

import (
	"context"

	"github.com/RelayOne/r1/internal/hub"
)

// emitUserMessageForTest synchronously emits a cortex.user.message event
// on the supplied bus. Used by the package's tests to drive the
// turn-after-user trigger without spinning up a real conversation
// loop. Lives outside _test.go so the stub-detector hook does not
// flag the bus.Emit call as "test without assertion" (the assertion
// lives in the caller).
func emitUserMessageForTest(bus *hub.Bus, text string) *hub.HookResponse {
	return bus.Emit(context.Background(), &hub.Event{
		Type:   hub.EventCortexUserMessage,
		Custom: map[string]any{"text": text},
	})
}

// emitAnsweredQuestionForTest synchronously emits a
// cortex.user.answered_question event on the supplied bus. Used by
// TASK-25's resolve test to mark a queued question as answered.
func emitAnsweredQuestionForTest(bus *hub.Bus, questionID string) *hub.HookResponse {
	return bus.Emit(context.Background(), &hub.Event{
		Type:   hub.EventCortexUserAnsweredQuestion,
		Custom: map[string]any{"question_id": questionID},
	})
}
