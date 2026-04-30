package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type AskUserConfig struct {
	QuestionID            string          `json:"question_id"`
	QuestionText          string          `json:"question_text"`
	AnswerSchema          json.RawMessage `json:"answer_schema"`
	Context               json.RawMessage `json:"context,omitempty"`
	HeadlessReasonerSkill string          `json:"headless_reasoner_skill,omitempty"`
	CacheKey              json.RawMessage `json:"cache_key"`
	AlwaysInteractive     bool            `json:"always_interactive,omitempty"`
}

type AskUserOutputs struct {
	Answer            json.RawMessage `json:"answer"`
	OperatorConfirmed bool            `json:"operator_confirmed"`
	Mode              string          `json:"mode"`
	LLMReasoning      string          `json:"llm_reasoning,omitempty"`
	LLMConfidence     float64         `json:"llm_confidence,omitempty"`
	AnsweredAt        time.Time       `json:"answered_at"`
}

type Prompter interface {
	Prompt(ctx context.Context, q AskUserConfig) (operatorAnswer string, confirmed bool, err error)
}

type HeadlessReasoner interface {
	Reason(ctx context.Context, q AskUserConfig, accumulatedContext json.RawMessage) (answer json.RawMessage, reasoning string, confidence float64, err error)
}

type ConstitutionPolicy struct {
	AlwaysInteractiveQuestions []string
	DefaultMode                string
}

func (p *ConstitutionPolicy) ShouldForceInteractive(questionID string) bool {
	for _, pattern := range p.AlwaysInteractiveQuestions {
		if pattern == questionID {
			return true
		}
		if strings.HasSuffix(pattern, ".*") {
			prefix := strings.TrimSuffix(pattern, ".*")
			if questionID == prefix || strings.HasPrefix(questionID, prefix+".") {
				return true
			}
		}
	}
	return false
}

type ExecuteOpts struct {
	Mode               string
	Prompter           Prompter
	Reasoner           HeadlessReasoner
	ConstitutionPolicy *ConstitutionPolicy
	CachedAnswer       *AskUserOutputs
}

func Execute(ctx context.Context, cfg AskUserConfig, opts ExecuteOpts) (AskUserOutputs, error) {
	if opts.CachedAnswer != nil {
		return *opts.CachedAnswer, nil
	}
	mode := opts.Mode
	if mode == "" {
		mode = "interactive"
	}
	if cfg.AlwaysInteractive || (opts.ConstitutionPolicy != nil && opts.ConstitutionPolicy.ShouldForceInteractive(cfg.QuestionID)) {
		mode = "interactive"
	}
	switch mode {
	case "interactive":
		if opts.Prompter == nil {
			return AskUserOutputs{}, errors.New("ask_user: interactive mode requires a prompter")
		}
		rawAnswer, confirmed, err := opts.Prompter.Prompt(ctx, cfg)
		if err != nil {
			return AskUserOutputs{}, fmt.Errorf("ask_user: prompt: %w", err)
		}
		answerJSON, err := json.Marshal(rawAnswer)
		if err != nil {
			return AskUserOutputs{}, fmt.Errorf("ask_user: marshal answer: %w", err)
		}
		return AskUserOutputs{Answer: answerJSON, OperatorConfirmed: confirmed, Mode: "operator", AnsweredAt: time.Now().UTC()}, nil
	case "headless", "hybrid":
		if opts.Reasoner == nil {
			return AskUserOutputs{}, errors.New("ask_user: headless mode requires a reasoner")
		}
		answer, reasoning, confidence, err := opts.Reasoner.Reason(ctx, cfg, cfg.Context)
		if err != nil {
			return AskUserOutputs{}, fmt.Errorf("ask_user: reason: %w", err)
		}
		if confidence < 0 || confidence > 1 {
			return AskUserOutputs{}, fmt.Errorf("ask_user: confidence out of range: %f", confidence)
		}
		return AskUserOutputs{Answer: answer, OperatorConfirmed: false, Mode: "llm-best-judgment", LLMReasoning: reasoning, LLMConfidence: confidence, AnsweredAt: time.Now().UTC()}, nil
	default:
		return AskUserOutputs{}, fmt.Errorf("ask_user: unknown mode %q", mode)
	}
}
