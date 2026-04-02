package model

import "strings"

type TaskType string

const (
	TaskTypePlan         TaskType = "plan"
	TaskTypeRefactor     TaskType = "refactor"
	TaskTypeTypeSafety   TaskType = "typesafety"
	TaskTypeDocs         TaskType = "docs"
	TaskTypeSecurity     TaskType = "security"
	TaskTypeArchitecture TaskType = "architecture"
	TaskTypeDevOps       TaskType = "devops"
	TaskTypeConcurrency  TaskType = "concurrency"
	TaskTypeReview       TaskType = "review"
)

// Provider identifies an execution engine.
type Provider string

const (
	ProviderClaude     Provider = "claude"     // Claude Code headless (subscription, $0 marginal)
	ProviderCodex      Provider = "codex"      // Codex CLI (subscription, $0 marginal)
	ProviderOpenRouter Provider = "openrouter" // OpenRouter API (multi-model, pay-per-token)
	ProviderDirectAPI  Provider = "direct-api" // Direct Anthropic/OpenAI API (pay-per-token)
	ProviderLintOnly   Provider = "lint-only"  // No model: just run build/test/lint
)

// Route defines the primary engine and full fallback chain per task type.
// Spec §10: Claude -> Codex -> OpenRouter -> Direct API -> lint-only
type Route struct {
	Primary       Provider
	FallbackChain []Provider // tried in order when primary is unavailable
	Reason        string
}

// Routes is the benchmark-backed routing table.
// Data sources: YUV.AI benchmarks, Terminal-Bench, Milvus code review study.
var Routes = map[TaskType]Route{
	TaskTypePlan: {
		Primary:       ProviderClaude,
		FallbackChain: []Provider{ProviderCodex, ProviderOpenRouter, ProviderDirectAPI},
		Reason:        "best at ambiguous prompts",
	},
	TaskTypeRefactor: {
		Primary:       ProviderClaude,
		FallbackChain: []Provider{ProviderCodex, ProviderOpenRouter, ProviderDirectAPI},
		Reason:        "4.9/5 refactoring (YUV.AI)",
	},
	TaskTypeTypeSafety: {
		Primary:       ProviderClaude,
		FallbackChain: []Provider{ProviderCodex, ProviderOpenRouter, ProviderDirectAPI},
		Reason:        "4.7/5 vs 4.2/5 (YUV.AI)",
	},
	TaskTypeDocs: {
		Primary:       ProviderClaude,
		FallbackChain: []Provider{ProviderCodex, ProviderOpenRouter, ProviderDirectAPI, ProviderLintOnly},
		Reason:        "4.9/5 vs 4.4/5 (YUV.AI)",
	},
	TaskTypeSecurity: {
		Primary:       ProviderClaude,
		FallbackChain: []Provider{ProviderCodex, ProviderOpenRouter, ProviderDirectAPI},
		Reason:        "53% solo detection, 100% precision (Milvus)",
	},
	TaskTypeArchitecture: {
		Primary:       ProviderCodex,
		FallbackChain: []Provider{ProviderClaude, ProviderOpenRouter, ProviderDirectAPI},
		Reason:        "4.8/5 vs 4.3/5 (YUV.AI)",
	},
	TaskTypeDevOps: {
		Primary:       ProviderCodex,
		FallbackChain: []Provider{ProviderClaude, ProviderOpenRouter, ProviderDirectAPI},
		Reason:        "Terminal-Bench 77.3% vs 65.4%",
	},
	TaskTypeConcurrency: {
		Primary:       ProviderCodex,
		FallbackChain: []Provider{ProviderClaude, ProviderOpenRouter, ProviderDirectAPI},
		Reason:        "Claude blind spot: 0/2 in Milvus study",
	},
	TaskTypeReview: {
		Primary:       ProviderCodex,
		FallbackChain: []Provider{ProviderClaude, ProviderOpenRouter, ProviderDirectAPI, ProviderLintOnly},
		Reason:        "GPT high recall first, Claude high precision second (BSWEN pipeline)",
	},
}

// Resolve returns the provider to use for a task type, considering availability.
// isAvailable is called for each provider in order until one returns true.
func Resolve(taskType TaskType, isAvailable func(Provider) bool) Provider {
	route, ok := Routes[taskType]
	if !ok {
		route = Routes[TaskTypeRefactor] // safe default
	}

	if isAvailable(route.Primary) {
		return route.Primary
	}

	for _, fb := range route.FallbackChain {
		if fb == ProviderLintOnly || isAvailable(fb) {
			return fb
		}
	}

	return ProviderLintOnly
}

// CrossModelReviewer returns the review provider for a given execute provider.
// Spec: "Claude implements -> GPT reviews. GPT implements -> Claude reviews."
func CrossModelReviewer(executeProvider Provider) Provider {
	switch executeProvider {
	case ProviderClaude:
		return ProviderCodex
	case ProviderCodex:
		return ProviderClaude
	default:
		return ProviderClaude // default reviewer
	}
}

func InferTaskType(task string) TaskType {
	s := strings.ToLower(task)
	switch {
	case containsAny(s, "architecture", "design", "schema", "topology"):
		return TaskTypeArchitecture
	case containsAny(s, "docker", "kubernetes", "k8s", "terraform", "deploy", "devops", "ci", "cd"):
		return TaskTypeDevOps
	case containsAny(s, "race", "concurrent", "deadlock", "mutex", "parallel"):
		return TaskTypeConcurrency
	case containsAny(s, "documentation", "readme", "docs", "comment"):
		return TaskTypeDocs
	case containsAny(s, "security", "auth", "oauth", "csrf", "xss", "secret"):
		return TaskTypeSecurity
	case containsAny(s, "types", "type", "typescript", "tsc", "typing"):
		return TaskTypeTypeSafety
	case containsAny(s, "review", "audit"):
		return TaskTypeReview
	default:
		return TaskTypeRefactor
	}
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
