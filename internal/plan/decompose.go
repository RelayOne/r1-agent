// decompose.go implements compound task decomposition.
// Inspired by OmX's task splitting: detect compound tasks (containing "and",
// numbered lists, multiple clauses) and decompose them into atomic subtasks.
//
// OmX also creates aspect-based subtasks for atomic tasks (implement, test, document).
// This improves parallelism and makes verification more granular.
package plan

import (
	"fmt"
	"regexp"
	"strings"
)

// DecompositionResult describes how a task was split.
type DecompositionResult struct {
	Original  Task   `json:"original"`
	Subtasks  []Task `json:"subtasks"`
	Strategy  string `json:"strategy"` // "numbered", "conjunction", "aspect", "none"
}

// Decompose splits a compound task into atomic subtasks.
// Returns the original task unchanged if it's already atomic.
func Decompose(task Task) DecompositionResult {
	desc := task.Description

	// Try numbered list first
	if subtasks := splitNumberedList(desc); len(subtasks) > 1 {
		return buildResult(task, subtasks, "numbered")
	}

	// Try bullet points
	if subtasks := splitBulletList(desc); len(subtasks) > 1 {
		return buildResult(task, subtasks, "bullets")
	}

	// Try conjunction splitting ("X and Y and Z")
	if subtasks := splitConjunctions(desc); len(subtasks) > 1 {
		return buildResult(task, subtasks, "conjunction")
	}

	// Try semicolon splitting
	if subtasks := splitSemicolons(desc); len(subtasks) > 1 {
		return buildResult(task, subtasks, "semicolons")
	}

	// Single task — optionally add aspect subtasks
	return DecompositionResult{
		Original: task,
		Subtasks: nil,
		Strategy: "none",
	}
}

// DecomposeWithAspects splits a task and optionally adds implementation aspects
// (implement, test, document) as subtasks of each decomposed piece.
func DecomposeWithAspects(task Task, addTests, addDocs bool) DecompositionResult {
	result := Decompose(task)

	if result.Strategy == "none" {
		// Atomic task — add aspects to the original
		var aspectTasks []Task
		aspectTasks = append(aspectTasks, Task{
			ID:          task.ID + "-impl",
			Description: "Implement: " + task.Description,
			Files:       task.Files,
			Type:        task.Type,
		})
		if addTests {
			aspectTasks = append(aspectTasks, Task{
				ID:           task.ID + "-test",
				Description:  "Write tests for: " + task.Description,
				Dependencies: []string{task.ID + "-impl"},
				Type:         "test",
			})
		}
		if addDocs {
			aspectTasks = append(aspectTasks, Task{
				ID:           task.ID + "-docs",
				Description:  "Document: " + task.Description,
				Dependencies: []string{task.ID + "-impl"},
				Type:         "docs",
			})
		}
		if len(aspectTasks) > 1 {
			return DecompositionResult{
				Original: task,
				Subtasks: aspectTasks,
				Strategy: "aspect",
			}
		}
		return result
	}

	// Compound task — add aspects to each subtask
	if addTests || addDocs {
		var enhanced []Task
		for _, sub := range result.Subtasks {
			enhanced = append(enhanced, sub)
			if addTests {
				enhanced = append(enhanced, Task{
					ID:           sub.ID + "-test",
					Description:  "Write tests for: " + sub.Description,
					Dependencies: []string{sub.ID},
					Type:         "test",
				})
			}
			if addDocs {
				enhanced = append(enhanced, Task{
					ID:           sub.ID + "-docs",
					Description:  "Document: " + sub.Description,
					Dependencies: []string{sub.ID},
					Type:         "docs",
				})
			}
		}
		result.Subtasks = enhanced
	}

	return result
}

// IsCompound returns true if a task description looks like it contains multiple tasks.
func IsCompound(description string) bool {
	result := Decompose(Task{ID: "check", Description: description})
	return result.Strategy != "none"
}

// EstimateComplexity estimates the number of subtasks a description would decompose into.
func EstimateComplexity(description string) int {
	result := Decompose(Task{ID: "est", Description: description})
	if len(result.Subtasks) > 0 {
		return len(result.Subtasks)
	}
	return 1
}

// --- Internal splitting strategies ---

var numberedRe = regexp.MustCompile(`(?m)^\s*\d+[\.\)]\s+(.+)`)

func splitNumberedList(desc string) []string {
	matches := numberedRe.FindAllStringSubmatch(desc, -1)
	if len(matches) < 2 {
		return nil
	}
	var items []string
	for _, m := range matches {
		item := strings.TrimSpace(m[1])
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

var bulletRe = regexp.MustCompile(`(?m)^\s*[-*]\s+(.+)`)

func splitBulletList(desc string) []string {
	matches := bulletRe.FindAllStringSubmatch(desc, -1)
	if len(matches) < 2 {
		return nil
	}
	var items []string
	for _, m := range matches {
		item := strings.TrimSpace(m[1])
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

func splitConjunctions(desc string) []string {
	// Only split on " and " when there are 2+ conjunctions or it's clearly a list
	lower := strings.ToLower(desc)
	count := strings.Count(lower, " and ")
	if count < 2 {
		return nil
	}

	parts := strings.Split(desc, " and ")
	var items []string
	for _, p := range parts {
		// Also split on commas within parts
		for _, sub := range strings.Split(p, ", ") {
			sub = strings.TrimSpace(sub)
			if sub != "" && len(sub) >= 5 { // skip tiny fragments
				items = append(items, sub)
			}
		}
	}
	if len(items) < 2 {
		return nil
	}
	return items
}

func splitSemicolons(desc string) []string {
	if !strings.Contains(desc, ";") {
		return nil
	}
	parts := strings.Split(desc, ";")
	var items []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			items = append(items, p)
		}
	}
	if len(items) < 2 {
		return nil
	}
	return items
}

func buildResult(task Task, descriptions []string, strategy string) DecompositionResult {
	var subtasks []Task
	for i, desc := range descriptions {
		subtasks = append(subtasks, Task{
			ID:          fmt.Sprintf("%s-%d", task.ID, i+1),
			Description: desc,
			Files:       task.Files, // inherit files
			Type:        task.Type,
		})
	}

	// Add dependency chain: each subtask depends on previous (for ordered lists)
	if strategy == "numbered" {
		for i := 1; i < len(subtasks); i++ {
			subtasks[i].Dependencies = []string{subtasks[i-1].ID}
		}
	}

	return DecompositionResult{
		Original: task,
		Subtasks: subtasks,
		Strategy: strategy,
	}
}
