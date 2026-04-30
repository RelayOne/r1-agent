package wizard

import (
	"fmt"
	"strings"
)

type Question struct {
	ID                string               `json:"id"`
	Stage             string               `json:"stage"`
	Text              string               `json:"text"`
	Help              string               `json:"help,omitempty"`
	AnswerSchema      AnswerSchema         `json:"answer_schema"`
	AlwaysInteractive bool                 `json:"always_interactive,omitempty"`
	IRPath            string               `json:"ir_path,omitempty"`
	DependsOn         []QuestionDependency `json:"depends_on,omitempty"`
}

type AnswerSchema struct {
	Type       string   `json:"type"`
	EnumValues []string `json:"enum_values,omitempty"`
	ListOf     string   `json:"list_of,omitempty"`
}

type QuestionDependency struct {
	QuestionID   string `json:"question_id"`
	OperatorMust string `json:"operator_must,omitempty"`
}

type Pack struct {
	ID          string     `json:"id"`
	Description string     `json:"description,omitempty"`
	Questions   []Question `json:"questions"`
}

func (p *Pack) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("pack: id required")
	}
	seen := make(map[string]bool, len(p.Questions))
	for i, q := range p.Questions {
		if q.ID == "" || q.Stage == "" || q.Text == "" {
			return fmt.Errorf("pack: question %d incomplete", i)
		}
		if seen[q.ID] {
			return fmt.Errorf("pack: duplicate question id %q", q.ID)
		}
		seen[q.ID] = true
		for _, dep := range q.DependsOn {
			if !seen[dep.QuestionID] {
				return fmt.Errorf("pack: question %q depends on unknown %q", q.ID, dep.QuestionID)
			}
		}
	}
	return nil
}

func (q Question) ShouldAsk(answered map[string]string) bool {
	if len(q.DependsOn) == 0 {
		return true
	}
	for _, dep := range q.DependsOn {
		answer, ok := answered[dep.QuestionID]
		if !ok {
			return false
		}
		if dep.OperatorMust != "" && !strings.EqualFold(strings.TrimSpace(answer), strings.TrimSpace(dep.OperatorMust)) {
			return false
		}
	}
	return true
}

func (q Question) ValidateAnswer(answer string) error {
	answer = strings.TrimSpace(answer)
	switch q.AnswerSchema.Type {
	case "", "text", "list":
		return nil
	case "enum":
		for _, allowed := range q.AnswerSchema.EnumValues {
			if strings.EqualFold(answer, allowed) {
				return nil
			}
		}
		return fmt.Errorf("question %q: %q is not one of [%s]", q.ID, answer, strings.Join(q.AnswerSchema.EnumValues, ", "))
	default:
		return fmt.Errorf("question %q: unsupported answer schema %q", q.ID, q.AnswerSchema.Type)
	}
}

func DefaultPack() *Pack {
	return &Pack{
		ID:          "default",
		Description: "Default authoring questions for deterministic skills.",
		Questions: []Question{
			{ID: "source.starting_point", Stage: "source", Text: "Are you starting from an existing skill, an external spec, or scratch?", AnswerSchema: AnswerSchema{Type: "enum", EnumValues: []string{"existing", "external", "scratch"}}},
			{ID: "source.format", Stage: "source", Text: "What format is the source in?", AnswerSchema: AnswerSchema{Type: "enum", EnumValues: []string{"r1-markdown-legacy", "openapi", "zapier", "codex-toml"}}, DependsOn: []QuestionDependency{{QuestionID: "source.starting_point", OperatorMust: "external"}}},
			{ID: "intent.purpose", Stage: "intent", Text: "In one sentence, what does this skill do?", AnswerSchema: AnswerSchema{Type: "text"}, IRPath: "description"},
			{ID: "caps.network.domains", Stage: "caps", Text: "Which domains does it call?", AnswerSchema: AnswerSchema{Type: "list", ListOf: "text"}, AlwaysInteractive: true, IRPath: "capabilities.network.allow_domains"},
			{ID: "caps.shell.commands", Stage: "caps", Text: "Which shell commands does it run?", AnswerSchema: AnswerSchema{Type: "list", ListOf: "text"}, AlwaysInteractive: true, IRPath: "capabilities.shell.allow_commands"},
			{ID: "caps.llm.budget", Stage: "caps", Text: "What's the maximum cost per execution in USD?", AnswerSchema: AnswerSchema{Type: "text"}, AlwaysInteractive: true, IRPath: "capabilities.llm.budget_usd"},
			{ID: "confirm.review_ir", Stage: "confirm", Text: "Does the constructed IR match your intent?", AnswerSchema: AnswerSchema{Type: "enum", EnumValues: []string{"yes", "no"}}},
		},
	}
}
