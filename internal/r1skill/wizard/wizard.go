package wizard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/r1skill/ir"
	"github.com/RelayOne/r1/internal/r1skill/wizard/adapter"
)

type RunOptions struct {
	SkillID      string
	Mode         string
	OperatorID   string
	QuestionPack *Pack
	SourcePath   string
	SourceFormat string
	Answers      map[string]string
}

type RunResult struct {
	Skill      *ir.Skill
	Decisions  *SkillAuthoringDecisions
	Source     *adapter.SourceArtifact
	SourcePath string
}

func Run(ctx context.Context, opts RunOptions) (*RunResult, error) {
	pack := opts.QuestionPack
	if pack == nil {
		pack = DefaultPack()
	}
	if err := pack.Validate(); err != nil {
		return nil, err
	}
	artifact, err := maybeLoadSource(ctx, opts.SourcePath, opts.SourceFormat)
	if err != nil {
		return nil, err
	}
	if opts.SkillID == "" {
		opts.SkillID = inferSkillID(opts.SourcePath)
	}
	now := time.Now().UTC()
	decisions := &SkillAuthoringDecisions{
		SessionID:      "wizard-session-" + now.Format("20060102T150405.000000000"),
		SkillID:        opts.SkillID,
		SkillVersion:   1,
		StartedAt:      now,
		Mode:           nonEmpty(opts.Mode, "interactive"),
		OperatorID:     opts.OperatorID,
		QuestionPackID: pack.ID,
		FinalStatus:    "registered",
		Version:        1,
	}
	skill := &ir.Skill{
		SchemaVersion: ir.SchemaVersion,
		SkillID:       opts.SkillID,
		SkillVersion:  1,
		Lineage: ir.Lineage{
			Kind:       "human",
			AuthoredAt: now,
		},
		Schemas: ir.Schemas{
			Inputs:  ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{}},
			Outputs: ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"result": {Type: "string"}}},
		},
		Graph: ir.Graph{
			Nodes: map[string]ir.Node{
				"describe": {
					Kind: "pure_fn",
					Config: mustJSON(map[string]any{
						"registry_ref": "wizard.describe",
						"input":        map[string]any{"kind": "literal", "value": opts.SkillID},
					}),
					Outputs: map[string]ir.TypeSpec{"result": {Type: "string"}},
				},
			},
			Return: ir.Expr{Kind: "ref", Ref: "describe"},
		},
	}
	answerMap := map[string]string{}
	for _, inf := range inferAnswersFromSource(artifact) {
		answerMap[inf.QuestionID] = inf.Answer
	}
	for k, v := range opts.Answers {
		answerMap[k] = v
	}
	for idx, q := range pack.Questions {
		answer := answerMap[q.ID]
		if answer == "" {
			answer = defaultAnswerFor(q)
		}
		if err := applyAnswer(skill, q, answer); err != nil {
			return nil, fmt.Errorf("wizard: apply %s: %w", q.ID, err)
		}
		decisions.Decisions = append(decisions.Decisions, Decision{
			Step:              idx + 1,
			Stage:             q.Stage,
			QuestionID:        q.ID,
			QuestionText:      q.Text,
			Mode:              decisionMode(opts.Mode),
			OperatorAnswer:    answer,
			InterpretedValue:  mustJSON(answer),
			IRPath:            nonEmpty(q.IRPath, "meta."+q.ID),
			IRValue:           mustIRValue(skill, q),
			OperatorConfirmed: opts.Mode != "headless",
			ConfirmedAt:       time.Now().UTC(),
		})
	}
	decisions.CompletedAt = time.Now().UTC()
	return &RunResult{Skill: skill, Decisions: decisions, Source: artifact, SourcePath: opts.SourcePath}, nil
}

type inferredAnswer struct {
	QuestionID string
	Answer     string
}

func inferAnswersFromSource(src *adapter.SourceArtifact) []inferredAnswer {
	if src == nil {
		return nil
	}
	out := []inferredAnswer{}
	for _, inf := range src.Inferences {
		switch inf.IRPath {
		case "description":
			if v, ok := inf.Value.(string); ok && v != "" {
				out = append(out, inferredAnswer{QuestionID: "intent.purpose", Answer: v})
			}
		case "capabilities.network.allow_domains":
			switch v := inf.Value.(type) {
			case []string:
				out = append(out, inferredAnswer{QuestionID: "caps.network.domains", Answer: strings.Join(v, ",")})
			case []any:
				parts := make([]string, 0, len(v))
				for _, item := range v {
					if s, ok := item.(string); ok {
						parts = append(parts, s)
					}
				}
				if len(parts) > 0 {
					out = append(out, inferredAnswer{QuestionID: "caps.network.domains", Answer: strings.Join(parts, ",")})
				}
			}
		}
	}
	return out
}

func maybeLoadSource(ctx context.Context, sourcePath, sourceFormat string) (*adapter.SourceArtifact, error) {
	if sourcePath == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, err
	}
	reg := adapter.Default
	var ad adapter.Adapter
	if sourceFormat != "" {
		ad, err = reg.Get(sourceFormat)
	} else {
		ad, err = reg.Detect(raw)
	}
	if err != nil {
		return nil, err
	}
	return ad.Parse(ctx, raw, sourcePath)
}

func applyAnswer(skill *ir.Skill, q Question, answer string) error {
	switch q.IRPath {
	case "":
		return nil
	case "description":
		skill.Description = answer
	case "capabilities.network.allow_domains":
		skill.Capabilities.Network.AllowDomains = splitCSV(answer)
		skill.Capabilities.Network.AllowMethods = []string{"GET", "POST"}
	case "capabilities.shell.allow_commands":
		skill.Capabilities.Shell.AllowCommands = splitCSV(answer)
	case "capabilities.llm.budget_usd":
		if answer == "" {
			return nil
		}
		f, err := strconv.ParseFloat(answer, 64)
		if err != nil {
			return err
		}
		skill.Capabilities.LLM.BudgetUSD = f
		skill.Capabilities.LLM.MaxCalls = 1
	default:
		return nil
	}
	return nil
}

func defaultAnswerFor(q Question) string {
	switch q.ID {
	case "source.starting_point":
		return "scratch"
	case "source.format":
		return "custom"
	case "intent.purpose":
		return "Generated deterministic skill"
	case "confirm.review_ir":
		return "yes"
	default:
		return ""
	}
}

func mustIRValue(skill *ir.Skill, q Question) json.RawMessage {
	switch q.IRPath {
	case "description":
		return mustJSON(skill.Description)
	case "capabilities.network.allow_domains":
		return mustJSON(skill.Capabilities.Network.AllowDomains)
	case "capabilities.shell.allow_commands":
		return mustJSON(skill.Capabilities.Shell.AllowCommands)
	case "capabilities.llm.budget_usd":
		return mustJSON(skill.Capabilities.LLM.BudgetUSD)
	default:
		return mustJSON(nil)
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func decisionMode(mode string) string {
	if mode == "headless" {
		return "llm-best-judgment"
	}
	return "operator"
}

func inferSkillID(sourcePath string) string {
	if sourcePath == "" {
		return "skill-wizard-generated"
	}
	base := filepath.Base(sourcePath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.ReplaceAll(base, "_", "-")
	return base
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
