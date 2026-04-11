package plan

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/stream"
)

// Reuse the mockProvider type defined in sow_convert_test.go (same package).

func goodSOW() *SOW {
	return &SOW{
		ID:   "ok",
		Name: "OK Project",
		Sessions: []Session{
			{
				ID: "S1", Title: "Foundation",
				Tasks: []Task{{ID: "T1", Description: "init"}},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC1", Description: "build", Command: "go build ./..."},
				},
			},
		},
	}
}

func TestCritiqueSOW_HappyPath(t *testing.T) {
	critJSON := `{
  "overall_score": 85,
  "dimensions": {"foundation": 90, "decomposition": 85, "criteria": 80, "stack": 85, "dependencies": 85, "specificity": 85},
  "issues": [],
  "verdict": "ship",
  "summary": "looks good"
}`
	prov := &mockProvider{name: "mock", response: critJSON}
	crit, err := CritiqueSOW(goodSOW(), prov, "m")
	if err != nil {
		t.Fatalf("CritiqueSOW: %v", err)
	}
	if crit.OverallScore != 85 {
		t.Errorf("score = %d", crit.OverallScore)
	}
	if crit.Verdict != "ship" {
		t.Errorf("verdict = %q", crit.Verdict)
	}
	if crit.HasBlocking() {
		t.Error("should have no blocking issues")
	}
}

func TestCritiqueSOW_WithBlockingIssues(t *testing.T) {
	critJSON := `{
  "overall_score": 55,
  "dimensions": {"foundation": 40, "decomposition": 60, "criteria": 30, "stack": 80, "dependencies": 80, "specificity": 60},
  "issues": [
    {"severity": "blocking", "session_id": "S1", "description": "no verifiable criteria", "fix": "add a build command"},
    {"severity": "major", "description": "foundation missing", "fix": "add setup session"}
  ],
  "verdict": "refine",
  "summary": "needs work"
}`
	prov := &mockProvider{name: "mock", response: critJSON}
	crit, err := CritiqueSOW(goodSOW(), prov, "m")
	if err != nil {
		t.Fatalf("CritiqueSOW: %v", err)
	}
	if !crit.HasBlocking() {
		t.Error("expected blocking issue")
	}
	if crit.Verdict != "refine" {
		t.Errorf("verdict = %q", crit.Verdict)
	}
}

func TestCritiqueSOW_PropagatesProviderError(t *testing.T) {
	prov := &mockProvider{err: errors.New("upstream 500")}
	_, err := CritiqueSOW(goodSOW(), prov, "m")
	if err == nil || !strings.Contains(err.Error(), "upstream") {
		t.Errorf("expected provider error, got %v", err)
	}
}

func TestCritiqueSOW_RejectsNilProvider(t *testing.T) {
	_, err := CritiqueSOW(goodSOW(), nil, "m")
	if err == nil {
		t.Error("expected nil-provider error")
	}
}

func TestCritiqueSOW_RejectsNilSOW(t *testing.T) {
	_, err := CritiqueSOW(nil, &mockProvider{}, "m")
	if err == nil {
		t.Error("expected nil-SOW error")
	}
}

func TestRefineSOW_HappyPath(t *testing.T) {
	refined := `{
  "id": "ok",
  "name": "Refined",
  "sessions": [
    {
      "id": "S1", "title": "Setup",
      "tasks": [{"id": "T1", "description": "fixed"}],
      "acceptance_criteria": [{"id": "AC1", "description": "build", "command": "go build ./..."}]
    }
  ]
}`
	prov := &mockProvider{name: "mock", response: refined}
	sow, err := RefineSOW(goodSOW(), &SOWCritique{Verdict: "refine"}, prov, "m")
	if err != nil {
		t.Fatalf("RefineSOW: %v", err)
	}
	if sow.Name != "Refined" {
		t.Errorf("refined name = %q", sow.Name)
	}
}

func TestRefineSOW_RejectsInvalidSchema(t *testing.T) {
	// Response has no sessions — fails ValidateSOW.
	prov := &mockProvider{name: "mock", response: `{"id":"x","name":"x","sessions":[]}`}
	_, err := RefineSOW(goodSOW(), &SOWCritique{}, prov, "m")
	if err == nil {
		t.Error("expected validation error")
	}
}

func TestCritiqueAndRefine_ShipsOnFirstPass(t *testing.T) {
	shipResp := `{"overall_score":90,"dimensions":{"foundation":90,"decomposition":90,"criteria":90,"stack":90,"dependencies":90,"specificity":90},"issues":[],"verdict":"ship","summary":"ship it"}`
	prov := &mockProvider{name: "mock", response: shipResp}
	sow, crit, err := CritiqueAndRefine(goodSOW(), prov, "m", 3)
	if err != nil {
		t.Fatalf("CritiqueAndRefine: %v", err)
	}
	if sow == nil || crit == nil {
		t.Fatal("nil result")
	}
	if crit.Verdict != "ship" {
		t.Errorf("verdict = %q", crit.Verdict)
	}
}

// flipProvider returns different responses on sequential calls so we can
// simulate "critique → refine → critique ships" cycles.
type flipProvider struct {
	responses []string
	call      int
	err       error
}

func (f *flipProvider) Name() string { return "flip" }
func (f *flipProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.call >= len(f.responses) {
		return nil, errors.New("flipProvider: out of responses")
	}
	resp := f.responses[f.call]
	f.call++
	return &provider.ChatResponse{
		Content: []provider.ResponseContent{{Type: "text", Text: resp}},
	}, nil
}
func (f *flipProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	return f.Chat(req)
}

func TestCritiqueAndRefine_ShipsAfterRefinement(t *testing.T) {
	critRefine := `{"overall_score":60,"dimensions":{"foundation":50,"decomposition":60,"criteria":50,"stack":70,"dependencies":70,"specificity":60},"issues":[{"severity":"major","description":"criteria too vague","fix":"add commands"}],"verdict":"refine","summary":"fix criteria"}`
	refined := `{
  "id": "ok",
  "name": "Better",
  "sessions": [
    {
      "id": "S1", "title": "Setup",
      "tasks": [{"id": "T1", "description": "init"}],
      "acceptance_criteria": [{"id": "AC1", "description": "build", "command": "go build ./..."}]
    }
  ]
}`
	critShip := `{"overall_score":88,"dimensions":{"foundation":90,"decomposition":85,"criteria":90,"stack":85,"dependencies":85,"specificity":85},"issues":[],"verdict":"ship","summary":"now it ships"}`
	prov := &flipProvider{responses: []string{critRefine, refined, critShip}}
	sow, crit, err := CritiqueAndRefine(goodSOW(), prov, "m", 3)
	if err != nil {
		t.Fatalf("CritiqueAndRefine: %v", err)
	}
	if crit.Verdict != "ship" {
		t.Errorf("final verdict = %q, want ship", crit.Verdict)
	}
	if sow.Name != "Better" {
		t.Errorf("refined name not propagated: %q", sow.Name)
	}
	if prov.call != 3 {
		t.Errorf("expected 3 LLM calls (crit, refine, crit), got %d", prov.call)
	}
}

// When the critic rejects but refinement is impossible (mock returns
// the same reject critique even from RefineSOW so the parse fails),
// the error chain still surfaces "rejected" so the caller can tell
// the difference between "buggy critique pipeline" and "critic actually
// rejected the work". This was the previous behavior — it is preserved
// because main.go pattern-matches on "rejected" for its warning text.
func TestCritiqueAndRefine_Rejects(t *testing.T) {
	rejectResp := `{"overall_score":10,"dimensions":{},"issues":[],"verdict":"reject","summary":"fundamentally broken"}`
	prov := &mockProvider{name: "mock", response: rejectResp}
	_, crit, err := CritiqueAndRefine(goodSOW(), prov, "m", 3)
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("expected rejection error, got %v", err)
	}
	if crit == nil || crit.Verdict != "reject" {
		t.Errorf("expected reject verdict")
	}
}

// When the critic rejects but a follow-up RefineSOW call produces a
// valid SOW that passes the next critique, CritiqueAndRefine returns
// the refined SOW with no error. This is the smart-loop fix: a reject
// is the strongest signal that refinement is needed, NOT a reason to
// bail out and let the caller proceed with the buggy SOW.
func TestCritiqueAndRefine_RefinesAfterReject(t *testing.T) {
	rejectCrit := `{"overall_score":40,"dimensions":{"foundation":40,"decomposition":40,"criteria":30,"stack":50,"dependencies":50,"specificity":40},"issues":[{"severity":"blocking","description":"acceptance criteria are grep checks that always pass","fix":"replace with real build commands"}],"verdict":"reject","summary":"too brittle to ship"}`
	refined := `{
  "id": "ok",
  "name": "Refined-after-reject",
  "sessions": [
    {
      "id": "S1", "title": "Setup",
      "tasks": [{"id": "T1", "description": "init"}],
      "acceptance_criteria": [{"id": "AC1", "description": "build", "command": "go build ./..."}]
    }
  ]
}`
	shipCrit := `{"overall_score":92,"dimensions":{"foundation":95,"decomposition":90,"criteria":95,"stack":90,"dependencies":90,"specificity":92},"issues":[],"verdict":"ship","summary":"now it ships"}`
	prov := &flipProvider{responses: []string{rejectCrit, refined, shipCrit}}
	sow, crit, err := CritiqueAndRefine(goodSOW(), prov, "m", 3)
	if err != nil {
		t.Fatalf("CritiqueAndRefine: %v", err)
	}
	if crit == nil || crit.Verdict != "ship" {
		t.Errorf("expected ship after refine; got %+v", crit)
	}
	if sow == nil || sow.Name != "Refined-after-reject" {
		t.Errorf("refined SOW not used: %+v", sow)
	}
	if prov.call != 3 {
		t.Errorf("expected 3 LLM calls (reject, refine, ship), got %d", prov.call)
	}
}

func TestCritiqueAndRefine_GivesUpAfterMaxPasses(t *testing.T) {
	// Every pass returns refine, never ships. After maxPasses we should
	// return the last refined SOW.
	refineResp := `{"overall_score":60,"dimensions":{},"issues":[{"severity":"major","description":"still bad","fix":"try again"}],"verdict":"refine","summary":"nope"}`
	refined := `{
  "id": "ok", "name": "Attempt",
  "sessions": [{"id":"S1","title":"t","tasks":[{"id":"T1","description":"x"}],"acceptance_criteria":[{"id":"AC1","description":"d","command":"true"}]}]
}`
	prov := &flipProvider{responses: []string{refineResp, refined, refineResp, refined}}
	_, crit, err := CritiqueAndRefine(goodSOW(), prov, "m", 2)
	if err != nil {
		t.Fatalf("should complete without error (just use the last refined): %v", err)
	}
	if crit == nil {
		t.Fatal("expected a critique")
	}
}

// ensure fmt is referenced so goimports keeps it if we use it later
var _ = json.RawMessage(nil)
