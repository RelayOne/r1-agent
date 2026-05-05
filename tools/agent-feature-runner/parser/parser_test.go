package parser

import (
	"strings"
	"testing"
)

const sampleFeature = `# tests/agent/web/foo.agent.feature.md

<!-- TAGS: smoke, web, chat -->
<!-- DEPENDS: r1d-server, web-chat-ui -->

## Scenario: User sends a message and sees a streamed response

- Given a fresh r1d daemon at "http://127.0.0.1:3948"
- And the web UI is loaded at "/"
- When I fill the textbox with name "Message" with "ping"
- And I click the button with name "Send"
- Then within 5 seconds the chat log contains an assistant message matching "pong|ping"

## Tool mapping (informative, runner derives automatically)
- "loaded at" -> r1.web.navigate
- "fill the textbox" -> r1.web.fill
- "click the button" -> r1.web.click
`

func TestParse_TitleAndTags(t *testing.T) {
	feat, err := Parse(strings.NewReader(sampleFeature), "test.feature.md")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if feat.Title != "tests/agent/web/foo.agent.feature.md" {
		t.Errorf("Title = %q", feat.Title)
	}
	wantTags := []string{"smoke", "web", "chat"}
	if len(feat.Tags) != len(wantTags) {
		t.Fatalf("Tags = %v, want %v", feat.Tags, wantTags)
	}
	for i, w := range wantTags {
		if feat.Tags[i] != w {
			t.Errorf("Tags[%d] = %q, want %q", i, feat.Tags[i], w)
		}
	}
}

func TestParse_Depends(t *testing.T) {
	feat, _ := Parse(strings.NewReader(sampleFeature), "x")
	want := []string{"r1d-server", "web-chat-ui"}
	if len(feat.Depends) != len(want) {
		t.Fatalf("Depends = %v, want %v", feat.Depends, want)
	}
}

func TestParse_OneScenarioWithFiveSteps(t *testing.T) {
	feat, _ := Parse(strings.NewReader(sampleFeature), "x")
	if len(feat.Scenarios) != 1 {
		t.Fatalf("got %d scenarios, want 1", len(feat.Scenarios))
	}
	sc := feat.Scenarios[0]
	if sc.Name != "User sends a message and sees a streamed response" {
		t.Errorf("Scenario.Name = %q", sc.Name)
	}
	if len(sc.Steps) != 5 {
		t.Fatalf("got %d steps, want 5", len(sc.Steps))
	}
}

func TestParse_StepKeywords(t *testing.T) {
	feat, _ := Parse(strings.NewReader(sampleFeature), "x")
	sc := feat.Scenarios[0]
	wantKeywords := []string{"Given", "And", "When", "And", "Then"}
	for i, w := range wantKeywords {
		if sc.Steps[i].Keyword != w {
			t.Errorf("Steps[%d].Keyword = %q, want %q", i, sc.Steps[i].Keyword, w)
		}
	}
}

func TestParse_StepLineNumbers(t *testing.T) {
	feat, _ := Parse(strings.NewReader(sampleFeature), "x")
	sc := feat.Scenarios[0]
	for i, step := range sc.Steps {
		if step.Line == 0 {
			t.Errorf("Steps[%d].Line is 0; should be 1-based", i)
		}
	}
	// Lines should be monotonic.
	for i := 1; i < len(sc.Steps); i++ {
		if sc.Steps[i].Line <= sc.Steps[i-1].Line {
			t.Errorf("Steps[%d] line %d not after Steps[%d] line %d",
				i, sc.Steps[i].Line, i-1, sc.Steps[i-1].Line)
		}
	}
}

func TestParse_ToolMappingRecorded(t *testing.T) {
	feat, _ := Parse(strings.NewReader(sampleFeature), "x")
	if len(feat.ToolMapping) != 3 {
		t.Errorf("ToolMapping has %d entries, want 3: %+v", len(feat.ToolMapping), feat.ToolMapping)
	}
	if feat.ToolMapping["loaded at"] != "r1.web.navigate" {
		t.Errorf(`ToolMapping["loaded at"] = %q, want r1.web.navigate`,
			feat.ToolMapping["loaded at"])
	}
	if feat.ToolMapping["click the button"] != "r1.web.click" {
		t.Errorf(`ToolMapping["click the button"] = %q`, feat.ToolMapping["click the button"])
	}
}

func TestParse_MultipleScenarios(t *testing.T) {
	src := sampleFeature + `

## Scenario: Second scenario

- Given a session with id "s-1"
- When I do something
- Then it works
`
	feat, _ := Parse(strings.NewReader(src), "x")
	if len(feat.Scenarios) != 2 {
		t.Fatalf("got %d scenarios, want 2", len(feat.Scenarios))
	}
	if feat.Scenarios[1].Name != "Second scenario" {
		t.Errorf("second scenario name = %q", feat.Scenarios[1].Name)
	}
	if len(feat.Scenarios[1].Steps) != 3 {
		t.Errorf("second scenario steps = %d, want 3", len(feat.Scenarios[1].Steps))
	}
}

func TestParse_ToolMappingAcceptsUnicodeArrow(t *testing.T) {
	src := `## Tool mapping
- "foo" → r1.web.navigate
`
	feat, _ := Parse(strings.NewReader(src), "x")
	if feat.ToolMapping["foo"] != "r1.web.navigate" {
		t.Errorf("unicode arrow not accepted: %+v", feat.ToolMapping)
	}
}

func TestParse_NoScenariosReturnsEmpty(t *testing.T) {
	src := `# Just a header

<!-- TAGS: empty -->
`
	feat, _ := Parse(strings.NewReader(src), "x")
	if len(feat.Scenarios) != 0 {
		t.Errorf("expected zero scenarios; got %d", len(feat.Scenarios))
	}
}

func TestParse_MalformedStepIgnored(t *testing.T) {
	// Random list items that aren't Given/When/Then/And are skipped.
	src := `## Scenario: demo

- random text without keyword
- Given a real step
- another random list item
- Then a real assertion
`
	feat, _ := Parse(strings.NewReader(src), "x")
	if len(feat.Scenarios) != 1 {
		t.Fatalf("got %d scenarios", len(feat.Scenarios))
	}
	if len(feat.Scenarios[0].Steps) != 2 {
		t.Errorf("Steps count = %d, want 2 (random lines should be skipped)",
			len(feat.Scenarios[0].Steps))
	}
}
