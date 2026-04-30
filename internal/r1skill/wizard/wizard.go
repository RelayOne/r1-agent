package wizard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	Stdin        io.Reader
	Stdout       io.Writer
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
	inferredAnswers := map[string]inferredAnswer{}
	for _, inf := range inferAnswersFromSource(artifact) {
		inferredAnswers[inf.QuestionID] = inf
	}
	resolvedAnswers, err := resolveAnswers(ctx, pack, inferredAnswers, opts)
	if err != nil {
		return nil, err
	}
	answerMap := map[string]string{}
	for idx, q := range pack.Questions {
		resolved, ok := resolvedAnswers[q.ID]
		if !ok {
			continue
		}
		answerMap[q.ID] = resolved.Answer
		if err := applyAnswer(skill, q, resolved.Answer); err != nil {
			return nil, fmt.Errorf("wizard: apply %s: %w", q.ID, err)
		}
		decisions.Decisions = append(decisions.Decisions, Decision{
			Step:              idx + 1,
			Stage:             q.Stage,
			QuestionID:        q.ID,
			QuestionText:      q.Text,
			Mode:              resolved.DecisionMode,
			OperatorAnswer:    resolved.Answer,
			LLMReasoning:      resolved.Reasoning,
			LLMConfidence:     resolved.Confidence,
			InterpretedValue:  mustJSON(resolved.Answer),
			IRPath:            nonEmpty(q.IRPath, "meta."+q.ID),
			IRValue:           mustIRValue(skill, q),
			OperatorConfirmed: resolved.OperatorConfirmed,
			ConfirmedAt:       time.Now().UTC(),
		})
	}
	decisions.CompletedAt = time.Now().UTC()
	return &RunResult{Skill: skill, Decisions: decisions, Source: artifact, SourcePath: opts.SourcePath}, nil
}

type inferredAnswer struct {
	QuestionID string
	Answer     string
	Confidence float64
	Source     string
}

type resolvedAnswer struct {
	Answer            string
	DecisionMode      string
	OperatorConfirmed bool
	Confidence        float64
	Reasoning         string
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
				out = append(out, inferredAnswer{QuestionID: "intent.purpose", Answer: v, Confidence: inf.Confidence, Source: inf.Source})
			}
		case "capabilities.network.allow_domains":
			switch v := inf.Value.(type) {
			case []string:
				out = append(out, inferredAnswer{QuestionID: "caps.network.domains", Answer: strings.Join(v, ","), Confidence: inf.Confidence, Source: inf.Source})
			case []any:
				parts := make([]string, 0, len(v))
				for _, item := range v {
					if s, ok := item.(string); ok {
						parts = append(parts, s)
					}
				}
				if len(parts) > 0 {
					out = append(out, inferredAnswer{QuestionID: "caps.network.domains", Answer: strings.Join(parts, ","), Confidence: inf.Confidence, Source: inf.Source})
				}
			}
		}
	}
	return out
}

func resolveAnswers(ctx context.Context, pack *Pack, inferred map[string]inferredAnswer, opts RunOptions) (map[string]resolvedAnswer, error) {
	answers := make(map[string]resolvedAnswer, len(pack.Questions))
	answeredValues := map[string]string{}
	mode := normalizedMode(opts.Mode)
	prompter := newPrompter(opts.Stdin, opts.Stdout)
	for _, q := range pack.Questions {
		if !q.ShouldAsk(answeredValues) {
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if provided, ok := opts.Answers[q.ID]; ok {
			if err := q.ValidateAnswer(provided); err != nil {
				return nil, err
			}
			answers[q.ID] = resolvedAnswer{
				Answer:            strings.TrimSpace(provided),
				DecisionMode:      "operator",
				OperatorConfirmed: true,
			}
			answeredValues[q.ID] = strings.TrimSpace(provided)
			continue
		}
		inferredAnswer, hasInference := inferred[q.ID]
		defaultAnswer := defaultAnswerFor(q)
		shouldPrompt := mode == "interactive" || (mode == "hybrid" && (q.AlwaysInteractive || (!hasInference && defaultAnswer == "")))
		if shouldPrompt {
			answer, err := prompter.ask(ctx, q, inferredAnswer.Answer, defaultAnswer)
			if err != nil {
				return nil, err
			}
			answers[q.ID] = resolvedAnswer{
				Answer:            answer,
				DecisionMode:      "operator",
				OperatorConfirmed: true,
			}
			answeredValues[q.ID] = answer
			continue
		}
		if hasInference {
			answers[q.ID] = resolvedAnswer{
				Answer:            inferredAnswer.Answer,
				DecisionMode:      "llm-best-judgment",
				OperatorConfirmed: false,
				Confidence:        inferredAnswer.Confidence,
				Reasoning:         "Inferred from source artifact: " + nonEmpty(inferredAnswer.Source, "adapter"),
			}
			answeredValues[q.ID] = inferredAnswer.Answer
			continue
		}
		if err := q.ValidateAnswer(defaultAnswer); err != nil {
			return nil, err
		}
		answers[q.ID] = resolvedAnswer{
			Answer:            defaultAnswer,
			DecisionMode:      decisionMode(mode),
			OperatorConfirmed: mode != "headless",
			Confidence:        defaultConfidenceFor(q, defaultAnswer),
			Reasoning:         defaultReasoningFor(q, defaultAnswer),
		}
		answeredValues[q.ID] = defaultAnswer
	}
	return answers, nil
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
		return ""
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

func normalizedMode(mode string) string {
	switch mode {
	case "headless", "hybrid", "interactive":
		return mode
	default:
		return "interactive"
	}
}

func defaultConfidenceFor(q Question, answer string) float64 {
	if strings.TrimSpace(answer) == "" {
		return 0
	}
	if q.AlwaysInteractive {
		return 0
	}
	return 0.35
}

func defaultReasoningFor(q Question, answer string) string {
	if strings.TrimSpace(answer) == "" {
		return ""
	}
	if q.AlwaysInteractive {
		return ""
	}
	return "Applied wizard default answer"
}

type promptSession struct {
	reader *bufio.Reader
	writer io.Writer
}

func newPrompter(stdin io.Reader, stdout io.Writer) *promptSession {
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = io.Discard
	}
	return &promptSession{
		reader: bufio.NewReader(stdin),
		writer: stdout,
	}
}

func (p *promptSession) ask(ctx context.Context, q Question, inferred, fallback string) (string, error) {
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		fmt.Fprintf(p.writer, "[%s] %s\n", q.ID, q.Text)
		if inferred != "" {
			fmt.Fprintf(p.writer, "  inferred: %s\n", inferred)
		}
		if fallback != "" {
			fmt.Fprintf(p.writer, "  default: %s\n", fallback)
		}
		if len(q.AnswerSchema.EnumValues) > 0 {
			fmt.Fprintf(p.writer, "  options: %s\n", strings.Join(q.AnswerSchema.EnumValues, ", "))
		}
		fmt.Fprint(p.writer, "> ")
		line, err := p.reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		answer := strings.TrimSpace(line)
		if answer == "" {
			switch {
			case inferred != "":
				answer = inferred
			case fallback != "":
				answer = fallback
			}
		}
		if err := q.ValidateAnswer(answer); err != nil {
			fmt.Fprintf(p.writer, "  invalid answer: %v\n", err)
			if err == io.EOF {
				return "", err
			}
			continue
		}
		return answer, nil
	}
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
