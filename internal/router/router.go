// Package router classifies natural-language operator input into a
// TaskType and dispatches it to the registered executor. Track B
// Task 19 keeps the classifier heuristic (regex on keywords) so
// the wiring lands without an LLM dependency; the interface is
// shaped so a future LLM-based classifier slots in without API
// changes.
package router

import (
	"errors"
	"regexp"
	"strings"

	"github.com/RelayOne/r1/internal/executor"
)

// ErrNoExecutor is returned by Dispatch when the router has no
// executor registered for the classified task type. Callers treat
// this as "classification succeeded but nothing can run it yet"
// and surface a helpful message.
var ErrNoExecutor = errors.New("router: no executor registered for classified task type")

// ErrEmptyInput is returned when Classify/Dispatch is called with
// whitespace-only input. The `stoke task` subcommand catches it
// and prints usage.
var ErrEmptyInput = errors.New("router: empty input")

// Router classifies natural-language input into a TaskType and
// dispatches to the registered executor.
type Router struct {
	classifier func(input string) executor.TaskType
	executors  map[executor.TaskType]executor.Executor
}

// New returns a Router with the default heuristic classifier and
// no executors registered.
func New() *Router {
	return &Router{
		classifier: DefaultClassifier,
		executors:  map[executor.TaskType]executor.Executor{},
	}
}

// SetClassifier swaps in a custom classifier. Exposed so tests can
// inject deterministic behavior and so a future LLM-based
// classifier replaces DefaultClassifier without changing the
// router's shape.
func (r *Router) SetClassifier(fn func(input string) executor.TaskType) {
	if fn == nil {
		fn = DefaultClassifier
	}
	r.classifier = fn
}

// Register associates a TaskType with its executor. Last-write
// wins — callers can replace an executor at runtime without
// re-constructing the router.
func (r *Router) Register(t executor.TaskType, e executor.Executor) {
	r.executors[t] = e
}

// Classify returns the TaskType for the given input using the
// configured classifier. Whitespace-only input returns TaskUnknown.
func (r *Router) Classify(input string) executor.TaskType {
	if strings.TrimSpace(input) == "" {
		return executor.TaskUnknown
	}
	return r.classifier(input)
}

// Dispatch looks up the executor for the input's classified type
// and returns it alongside the TaskType. Returns ErrNoExecutor when
// no executor is registered, and ErrEmptyInput for blank input.
func (r *Router) Dispatch(input string) (executor.Executor, executor.TaskType, error) {
	if strings.TrimSpace(input) == "" {
		return nil, executor.TaskUnknown, ErrEmptyInput
	}
	tt := r.classifier(input)
	e, ok := r.executors[tt]
	if !ok {
		return nil, tt, ErrNoExecutor
	}
	return e, tt, nil
}

// Classification regexes. Kept at package scope so they compile
// once. Order matters — DefaultClassifier checks in this priority:
// deploy > research > browser > delegate > code.
var (
	deployRegex   = regexp.MustCompile(`(?i)\b(deploy|provision|fly\.io|vercel|cloudflare|kamal|docker\s*(compose|image))\b`)
	researchRegex = regexp.MustCompile(`(?i)\b(research|find out|compare|survey|what (is|are)|how does .* work)\b`)
	browserRegex  = regexp.MustCompile(`(?i)\b(browse|navigate|screenshot|click|fill out|submit)\b`)
	delegateRegex = regexp.MustCompile(`(?i)\b(hire|delegate|outsource|translate|generate (image|logo|icon))\b`)
)

// DefaultClassifier is the MVP heuristic classifier. Matches in
// priority order — the first regex that hits wins. When nothing
// matches, returns TaskCode as the safe default since Stoke's
// trunk use case is code.
func DefaultClassifier(input string) executor.TaskType {
	low := strings.ToLower(input)
	switch {
	case deployRegex.MatchString(low):
		return executor.TaskDeploy
	case researchRegex.MatchString(low):
		return executor.TaskResearch
	case browserRegex.MatchString(low):
		return executor.TaskBrowser
	case delegateRegex.MatchString(low):
		return executor.TaskDelegate
	case strings.Contains(low, ".md") && (strings.Contains(low, "sow") || strings.Contains(low, "spec")):
		return executor.TaskCode
	default:
		return executor.TaskCode
	}
}
