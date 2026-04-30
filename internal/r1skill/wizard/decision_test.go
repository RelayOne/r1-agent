package wizard

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDecisionsJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	log := SkillAuthoringDecisions{
		SessionID:      "s1",
		SkillID:        "skill",
		SkillVersion:   1,
		StartedAt:      now,
		CompletedAt:    now,
		Mode:           "interactive",
		QuestionPackID: "default",
		FinalStatus:    "registered",
		Version:        1,
		Decisions: []Decision{{
			Step: 1, Stage: "intent", QuestionID: "intent.purpose", QuestionText: "What?", Mode: "operator",
			InterpretedValue: json.RawMessage(`"x"`), IRPath: "description", IRValue: json.RawMessage(`"x"`), OperatorConfirmed: true, ConfirmedAt: now,
		}},
	}
	data, err := json.Marshal(log)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SkillAuthoringDecisions
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionsQueryByQuestionID(t *testing.T) {
	log := SkillAuthoringDecisions{Decisions: []Decision{
		{QuestionID: "caps.shell.commands", IRPath: "capabilities.shell.allow_commands", IRValue: json.RawMessage(`[]`), Stage: "caps", Mode: "operator"},
		{QuestionID: "intent.purpose", IRPath: "description", IRValue: json.RawMessage(`"x"`), Stage: "intent", Mode: "operator"},
	}}
	got := log.Filter(Query{QuestionID: "caps.shell.commands"})
	if len(got) != 1 {
		t.Fatalf("expected 1 match, got %d", len(got))
	}
}
