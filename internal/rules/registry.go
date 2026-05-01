package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/google/uuid"
)

const (
	schemaVersion = 1

	ScopeGlobal = "global"
	ScopeRepo   = "repo"
	ScopeTask   = "task"

	StatusActive = "active"
	StatusPaused = "paused"

	StrategyRegexFilter      = "regex_filter"
	StrategyArgumentValidate = "argument_validator"
	StrategySubagentCheck    = "subagent_check"

	VerdictPass  Verdict = "PASS"
	VerdictWarn  Verdict = "WARN"
	VerdictBlock Verdict = "BLOCK"
)

type Verdict string

type ImpactMetrics struct {
	Invocations   int64   `json:"invocations"`
	Allowed       int64   `json:"allowed"`
	Blocked       int64   `json:"blocked"`
	Warnings      int64   `json:"warnings"`
	AvgCheckMS    float64 `json:"avg_check_ms"`
	AvgTokensUsed float64 `json:"avg_tokens_used"`
}

type RegexFilterSpec struct {
	Target  string `json:"target"`
	Pattern string `json:"pattern"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

type ArgumentConstraint struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

type ArgumentValidatorSpec struct {
	MatchAll    bool                 `json:"match_all"`
	Constraints []ArgumentConstraint `json:"constraints"`
	Verdict     string               `json:"verdict"`
	Reason      string               `json:"reason"`
}

type SubagentCheckSpec struct {
	SummaryTemplate string `json:"summary_template"`
	DefaultVerdict  string `json:"default_verdict"`
	Reason          string `json:"reason"`
}

type EnforcementConfig struct {
	RegexFilter       *RegexFilterSpec       `json:"regex_filter,omitempty"`
	ArgumentValidator *ArgumentValidatorSpec `json:"argument_validator,omitempty"`
	SubagentCheck     *SubagentCheckSpec     `json:"subagent_check,omitempty"`
}

type Rule struct {
	ID                  string            `json:"id"`
	Text                string            `json:"text"`
	CreatedAt           time.Time         `json:"created_at"`
	Scope               string            `json:"scope"`
	ToolFilter          string            `json:"tool_filter"`
	EnforcementStrategy string            `json:"enforcement_strategy"`
	Status              string            `json:"status"`
	ImpactMetrics       ImpactMetrics     `json:"impact_metrics"`
	EnforcementConfig   EnforcementConfig `json:"enforcement_config,omitempty"`
}

type HistoryEntry struct {
	Version   int       `json:"version"`
	ChangedAt time.Time `json:"changed_at"`
	Action    string    `json:"action"`
	RuleID    string    `json:"rule_id,omitempty"`
	Snapshot  *Rule     `json:"snapshot,omitempty"`
}

type fileDocument struct {
	SchemaVersion int            `json:"schema_version"`
	Version       int            `json:"version"`
	Rules         []Rule         `json:"rules"`
	History       []HistoryEntry `json:"history,omitempty"`
}

type AddRequest struct {
	Text             string
	Scope            string
	ToolFilter       string
	StrategyOverride string
}

type CheckContext struct {
	RepoRoot string
	TaskID   string
}

type Evaluation struct {
	RuleID     string  `json:"rule_id"`
	Verdict    Verdict `json:"verdict"`
	Reason     string  `json:"reason"`
	CheckMS    int64   `json:"check_ms"`
	TokensUsed int64   `json:"tokens_used"`
}

type CheckResult struct {
	Verdict     Verdict      `json:"verdict"`
	Reason      string       `json:"reason,omitempty"`
	Evaluations []Evaluation `json:"evaluations,omitempty"`
}

type Synthesizer interface {
	Synthesize(ctx context.Context, req SynthesisRequest) (SynthesisResult, error)
}

type Store interface {
	Load() (fileDocument, error)
	Save(doc fileDocument) error
}

type Registry struct {
	mu          sync.Mutex
	store       Store
	synthesizer Synthesizer
}

func NewRegistry(store Store, synth Synthesizer) *Registry {
	if synth == nil {
		synth = HeuristicSynthesizer{}
	}
	return &Registry{store: store, synthesizer: synth}
}

func NewRepoRegistry(repoRoot string, synth Synthesizer) *Registry {
	return NewRegistry(NewRepoStore(repoRoot), synth)
}

func NewFSRegistry(stateDir string, synth Synthesizer) *Registry {
	return NewRegistry(NewFSStore(stateDir), synth)
}

func NewRepoStore(repoRoot string) Store {
	return &repoStore{repoRoot: repoRoot}
}

func NewFSStore(stateDir string) Store {
	return &fsStore{path: filepath.Join(stateDir, "rules.json")}
}

func (r *Registry) Add(text string) (Rule, error) {
	return r.AddWithOptions(context.Background(), AddRequest{Text: text})
}

func (r *Registry) AddWithOptions(ctx context.Context, req AddRequest) (Rule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	text := strings.TrimSpace(req.Text)
	if text == "" {
		return Rule{}, errors.New("rule text required")
	}
	doc, err := r.loadLocked()
	if err != nil {
		return Rule{}, err
	}

	synthReq := SynthesisRequest{
		Text:             text,
		Scope:            normalizeScope(req.Scope),
		ToolFilter:       strings.TrimSpace(req.ToolFilter),
		StrategyOverride: strings.TrimSpace(req.StrategyOverride),
	}
	synth, err := r.synthesizer.Synthesize(ctx, synthReq)
	if err != nil {
		return Rule{}, err
	}
	rule := Rule{
		ID:                  "rule-" + uuid.NewString(),
		Text:                text,
		CreatedAt:           time.Now().UTC(),
		Scope:               synth.Scope,
		ToolFilter:          synth.ToolFilter,
		EnforcementStrategy: synth.Strategy,
		Status:              StatusActive,
		EnforcementConfig:   synth.Config,
	}
	doc.Rules = append(doc.Rules, rule)
	r.appendHistory(&doc, "add", &rule)
	if err := r.store.Save(doc); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

func (r *Registry) List() ([]Rule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	doc, err := r.loadLocked()
	if err != nil {
		return nil, err
	}
	out := append([]Rule(nil), doc.Rules...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (r *Registry) Get(id string) (Rule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	doc, err := r.loadLocked()
	if err != nil {
		return Rule{}, err
	}
	for _, rule := range doc.Rules {
		if rule.ID == id {
			return rule, nil
		}
	}
	return Rule{}, os.ErrNotExist
}

func (r *Registry) Delete(id string) error {
	return r.updateRule(id, "delete", func(doc *fileDocument, idx int) error {
		rule := doc.Rules[idx]
		doc.Rules = append(doc.Rules[:idx], doc.Rules[idx+1:]...)
		r.appendHistory(doc, "delete", &rule)
		return nil
	})
}

func (r *Registry) Pause(id string) error {
	return r.updateRule(id, "pause", func(doc *fileDocument, idx int) error {
		doc.Rules[idx].Status = StatusPaused
		rule := doc.Rules[idx]
		r.appendHistory(doc, "pause", &rule)
		return nil
	})
}

func (r *Registry) Resume(id string) error {
	return r.updateRule(id, "resume", func(doc *fileDocument, idx int) error {
		doc.Rules[idx].Status = StatusActive
		rule := doc.Rules[idx]
		r.appendHistory(doc, "resume", &rule)
		return nil
	})
}

func (r *Registry) Check(ctx context.Context, toolName string, toolArgs json.RawMessage, checkCtx CheckContext) (CheckResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	doc, err := r.loadLocked()
	if err != nil {
		return CheckResult{}, err
	}

	result := CheckResult{Verdict: VerdictPass}
	changed := false
	for i := range doc.Rules {
		rule := &doc.Rules[i]
		if rule.Status != StatusActive {
			continue
		}
		if !scopeApplies(rule.Scope, checkCtx) {
			continue
		}
		if !toolMatches(rule.ToolFilter, toolName) {
			continue
		}
		start := time.Now()
		verdict, reason, tokensUsed := evaluateRule(ctx, *rule, toolName, toolArgs, checkCtx)
		checkMS := time.Since(start).Milliseconds()
		if verdict == VerdictPass && reason == "" {
			reason = "allowed by user rule"
		}
		eval := Evaluation{
			RuleID:     rule.ID,
			Verdict:    verdict,
			Reason:     reason,
			CheckMS:    checkMS,
			TokensUsed: tokensUsed,
		}
		result.Evaluations = append(result.Evaluations, eval)
		updateMetrics(&rule.ImpactMetrics, verdict, checkMS, tokensUsed)
		changed = true
		switch verdict {
		case VerdictBlock:
			result.Verdict = VerdictBlock
			appendReason(&result, fmt.Sprintf("%s: %s", rule.ID, reason))
		case VerdictWarn:
			if result.Verdict != VerdictBlock {
				result.Verdict = VerdictWarn
			}
			appendReason(&result, fmt.Sprintf("%s: %s", rule.ID, reason))
		}
	}
	if changed {
		if err := r.store.Save(doc); err != nil {
			return CheckResult{}, err
		}
	}
	return result, nil
}

func (r *Registry) updateRule(id, action string, fn func(doc *fileDocument, idx int) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	doc, err := r.loadLocked()
	if err != nil {
		return err
	}
	for idx := range doc.Rules {
		if doc.Rules[idx].ID != id {
			continue
		}
		if err := fn(&doc, idx); err != nil {
			return err
		}
		if err := r.store.Save(doc); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("%s rule %q: %w", action, id, os.ErrNotExist)
}

func (r *Registry) loadLocked() (fileDocument, error) {
	if r.store == nil {
		return fileDocument{}, errors.New("rules registry store is nil")
	}
	doc, err := r.store.Load()
	if err != nil {
		return fileDocument{}, err
	}
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = schemaVersion
	}
	if doc.Version == 0 {
		doc.Version = 1
	}
	if doc.Rules == nil {
		doc.Rules = []Rule{}
	}
	return migrate(doc)
}

func (r *Registry) appendHistory(doc *fileDocument, action string, snapshot *Rule) {
	doc.Version++
	entry := HistoryEntry{
		Version:   doc.Version,
		ChangedAt: time.Now().UTC(),
		Action:    action,
	}
	if snapshot != nil {
		cp := *snapshot
		entry.RuleID = cp.ID
		entry.Snapshot = &cp
	}
	doc.History = append(doc.History, entry)
}

func migrate(doc fileDocument) (fileDocument, error) {
	for i := range doc.Rules {
		doc.Rules[i].Scope = normalizeScope(doc.Rules[i].Scope)
		if strings.TrimSpace(doc.Rules[i].ToolFilter) == "" {
			doc.Rules[i].ToolFilter = ".*"
		}
		if doc.Rules[i].Status == "" {
			doc.Rules[i].Status = StatusActive
		}
		if doc.Rules[i].CreatedAt.IsZero() {
			doc.Rules[i].CreatedAt = time.Now().UTC()
		}
	}
	doc.SchemaVersion = schemaVersion
	if doc.Rules == nil {
		doc.Rules = []Rule{}
	}
	return doc, nil
}

func normalizeScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case ScopeGlobal:
		return ScopeGlobal
	case ScopeTask:
		return ScopeTask
	case ScopeRepo, "":
		return ScopeRepo
	default:
		return ScopeRepo
	}
}

func toolMatches(filter, toolName string) bool {
	pattern := strings.TrimSpace(filter)
	if pattern == "" {
		pattern = ".*"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return pattern == toolName
	}
	return re.MatchString(toolName)
}

func scopeApplies(scope string, checkCtx CheckContext) bool {
	switch normalizeScope(scope) {
	case ScopeGlobal:
		return true
	case ScopeTask:
		return strings.TrimSpace(checkCtx.TaskID) != ""
	default:
		return true
	}
}

func appendReason(result *CheckResult, reason string) {
	if strings.TrimSpace(reason) == "" {
		return
	}
	if result.Reason == "" {
		result.Reason = reason
		return
	}
	result.Reason += "; " + reason
}

func updateMetrics(metrics *ImpactMetrics, verdict Verdict, checkMS, tokensUsed int64) {
	metrics.Invocations++
	switch verdict {
	case VerdictBlock:
		metrics.Blocked++
	case VerdictWarn:
		metrics.Warnings++
		metrics.Allowed++
	default:
		metrics.Allowed++
	}
	metrics.AvgCheckMS = rollingAverage(metrics.AvgCheckMS, float64(checkMS), metrics.Invocations)
	metrics.AvgTokensUsed = rollingAverage(metrics.AvgTokensUsed, float64(tokensUsed), metrics.Invocations)
}

func rollingAverage(current, sample float64, count int64) float64 {
	if count <= 1 {
		return sample
	}
	return current + ((sample - current) / float64(count))
}

type repoStore struct {
	repoRoot string
}

func (s *repoStore) Load() (fileDocument, error) {
	data, err := r1dir.ReadFileFor(s.repoRoot, "rules.json")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fileDocument{
				SchemaVersion: schemaVersion,
				Version:       1,
				Rules:         []Rule{},
			}, nil
		}
		return fileDocument{}, err
	}
	return parseDocument(data)
}

func (s *repoStore) Save(doc fileDocument) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return r1dir.WriteFileFor(s.repoRoot, "rules.json", data, 0o644)
}

type fsStore struct {
	path string
}

func (s *fsStore) Load() (fileDocument, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fileDocument{
				SchemaVersion: schemaVersion,
				Version:       1,
				Rules:         []Rule{},
			}, nil
		}
		return fileDocument{}, err
	}
	return parseDocument(data)
}

func (s *fsStore) Save(doc fileDocument) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

func parseDocument(data []byte) (fileDocument, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return fileDocument{
			SchemaVersion: schemaVersion,
			Version:       1,
			Rules:         []Rule{},
		}, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var rulesOnly []Rule
		if err := json.Unmarshal(data, &rulesOnly); err != nil {
			return fileDocument{}, err
		}
		return fileDocument{
			SchemaVersion: schemaVersion,
			Version:       1,
			Rules:         rulesOnly,
		}, nil
	}
	var doc fileDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return fileDocument{}, err
	}
	return doc, nil
}

func InferRepoRoot(path string) string {
	current := filepath.Clean(strings.TrimSpace(path))
	if current == "" {
		return "."
	}
	if info, err := os.Stat(current); err == nil && !info.IsDir() {
		current = filepath.Dir(current)
	}
	if base := filepath.Base(current); base == "worktrees" {
		parent := filepath.Dir(current)
		if dir := filepath.Base(parent); dir == r1dir.Canonical || dir == r1dir.Legacy {
			return filepath.Dir(parent)
		}
	}
	for {
		if looksLikeRepoRoot(current) {
			return current
		}
		next := filepath.Dir(current)
		if next == current {
			return path
		}
		current = next
	}
}

func looksLikeRepoRoot(path string) bool {
	for _, candidate := range []string{".git", r1dir.Canonical, r1dir.Legacy} {
		if _, err := os.Stat(filepath.Join(path, candidate)); err == nil {
			return true
		}
	}
	return false
}
