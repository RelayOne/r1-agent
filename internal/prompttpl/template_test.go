package prompttpl

import (
	"strings"
	"testing"
)

func TestSimpleVariable(t *testing.T) {
	tpl := MustParse("test", "Hello {{name}}!")
	result := tpl.Render(Vars{"name": "World"})
	if result != "Hello World!" {
		t.Errorf("expected 'Hello World!', got %q", result)
	}
}

func TestDefaultValue(t *testing.T) {
	tpl := MustParse("test", "Mode: {{mode|standard}}")
	result := tpl.Render(Vars{})
	if result != "Mode: standard" {
		t.Errorf("expected 'Mode: standard', got %q", result)
	}

	result = tpl.Render(Vars{"mode": "fast"})
	if result != "Mode: fast" {
		t.Errorf("expected 'Mode: fast', got %q", result)
	}
}

func TestIfBlock(t *testing.T) {
	tpl := MustParse("test", "Start{{#if debug}} [DEBUG]{{/if}} End")

	result := tpl.Render(Vars{"debug": true})
	if result != "Start [DEBUG] End" {
		t.Errorf("got %q", result)
	}

	result = tpl.Render(Vars{"debug": false})
	if result != "Start End" {
		t.Errorf("got %q", result)
	}
}

func TestIfElseBlock(t *testing.T) {
	tpl := MustParse("test", "{{#if premium}}Pro mode{{else}}Free mode{{/if}}")

	result := tpl.Render(Vars{"premium": true})
	if result != "Pro mode" {
		t.Errorf("got %q", result)
	}

	result = tpl.Render(Vars{"premium": false})
	if result != "Free mode" {
		t.Errorf("got %q", result)
	}
}

func TestEachBlock(t *testing.T) {
	tpl := MustParse("test", "Files:{{#each files as f}}\n- {{f}}{{/each}}")
	result := tpl.Render(Vars{"files": []string{"a.go", "b.go"}})

	if !strings.Contains(result, "- a.go") || !strings.Contains(result, "- b.go") {
		t.Errorf("got %q", result)
	}
}

func TestEachEmpty(t *testing.T) {
	tpl := MustParse("test", "Items:{{#each items as i}}{{i}}{{/each}}")
	result := tpl.Render(Vars{})
	if result != "Items:" {
		t.Errorf("expected 'Items:', got %q", result)
	}
}

func TestVariables(t *testing.T) {
	tpl := MustParse("test", "{{name}} is {{age}} and {{#if admin}}admin{{/if}}")
	vars := tpl.Variables()

	expected := map[string]bool{"name": true, "age": true, "admin": true}
	for _, v := range vars {
		if !expected[v] {
			t.Errorf("unexpected variable: %s", v)
		}
	}
	if len(vars) != 3 {
		t.Errorf("expected 3 variables, got %d: %v", len(vars), vars)
	}
}

func TestMissingVariable(t *testing.T) {
	tpl := MustParse("test", "Hello {{name}}!")
	result := tpl.Render(Vars{})
	if result != "Hello !" {
		t.Errorf("expected 'Hello !', got %q", result)
	}
}

func TestNestedIf(t *testing.T) {
	src := "{{#if a}}A{{#if b}}B{{/if}}{{/if}}"
	tpl := MustParse("test", src)

	result := tpl.Render(Vars{"a": true, "b": true})
	if result != "AB" {
		t.Errorf("expected AB, got %q", result)
	}

	result = tpl.Render(Vars{"a": true, "b": false})
	if result != "A" {
		t.Errorf("expected A, got %q", result)
	}
}

func TestTruthyValues(t *testing.T) {
	tests := []struct {
		val  any
		want bool
	}{
		{true, true},
		{false, false},
		{"hello", true},
		{"", false},
		{42, true},
		{0, false},
		{nil, false},
		{[]string{"a"}, true},
		{[]string{}, false},
	}

	for _, tt := range tests {
		if got := truthy(tt.val); got != tt.want {
			t.Errorf("truthy(%v) = %v, want %v", tt.val, got, tt.want)
		}
	}
}

func TestComplexTemplate(t *testing.T) {
	src := `You are {{role|assistant}}.
{{#if context}}Context: {{context}}
{{/if}}Task: {{task}}
{{#if constraints}}Constraints:
{{#each constraints as c}}- {{c}}
{{/each}}{{/if}}`

	tpl := MustParse("complex", src)
	result := tpl.Render(Vars{
		"role":        "architect",
		"task":        "Design the API",
		"constraints": []string{"REST only", "No breaking changes"},
	})

	if !strings.Contains(result, "architect") {
		t.Error("should contain role")
	}
	if !strings.Contains(result, "Design the API") {
		t.Error("should contain task")
	}
	if !strings.Contains(result, "- REST only") {
		t.Error("should contain constraint")
	}
	if strings.Contains(result, "Context:") {
		t.Error("should not contain context when not set")
	}
}

func TestParseError(t *testing.T) {
	_, err := Parse("bad", "{{#if x}}unclosed")
	if err == nil {
		t.Error("should error on unclosed if block")
	}
}
