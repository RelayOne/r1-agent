// todo_tools.go — todo_write and todo_read tool handlers.
//
// T-R1P-012: Plan mode / TODO list management tools — equivalent to Claude Code's
// TodoWrite and TodoRead. Agents maintain a structured, persistent task list across
// tool calls so the model can track progress, mark items done, and avoid losing work.
//
// Design:
//   - The todo list is stored in-memory per Registry instance (not persisted to disk)
//     unless the registry is created with a working directory, in which case it is
//     also written to .r1/todos.json for resumability.
//   - Each todo item has: id (string), content (string), status (pending|in_progress|done),
//     priority (high|medium|low), created_at (RFC3339).
//   - todo_write replaces the entire list atomically (mirrors TodoWrite semantics).
//   - todo_read returns the current list as formatted text.
//   - todo_update patches a single item's status or priority.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TodoStatus is the lifecycle state of a todo item.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoDone       TodoStatus = "done"
)

// TodoPriority classifies urgency.
type TodoPriority string

const (
	TodoHigh   TodoPriority = "high"
	TodoMedium TodoPriority = "medium"
	TodoLow    TodoPriority = "low"
)

// TodoItem is a single todo entry.
type TodoItem struct {
	ID        string       `json:"id"`
	Content   string       `json:"content"`
	Status    TodoStatus   `json:"status"`
	Priority  TodoPriority `json:"priority"`
	CreatedAt string       `json:"created_at"`
}

// todoStore is the in-process store for the Registry's todo list.
type todoStore struct {
	mu    sync.RWMutex
	items []TodoItem
}

// lazyTodos returns the registry's todoStore, initialising it on first call.
// Stored as a pointer field on Registry (added below as a lazy init via sync.Once).
func (r *Registry) lazyTodos() *todoStore {
	r.todoOnce.Do(func() {
		r.todos = &todoStore{}
		// Best-effort load from .r1/todos.json if the working directory has one.
		if r.workDir != "" {
			path := filepath.Join(r.workDir, ".r1", "todos.json")
			if data, err := os.ReadFile(path); err == nil {
				var items []TodoItem
				if json.Unmarshal(data, &items) == nil {
					r.todos.items = items
				}
			}
		}
	})
	return r.todos
}

// saveTodos persists the todo list to .r1/todos.json (best-effort; never errors).
func (r *Registry) saveTodos(items []TodoItem) {
	if r.workDir == "" {
		return
	}
	dir := filepath.Join(r.workDir, ".r1")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(dir, "todos.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		return
	}
}

// handleTodoWrite implements todo_write (T-R1P-012).
// Replaces the entire todo list with the provided items.
func (r *Registry) handleTodoWrite(input json.RawMessage) (string, error) {
	var args struct {
		Todos []struct {
			ID       string `json:"id"`
			Content  string `json:"content"`
			Status   string `json:"status"`
			Priority string `json:"priority"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if args.Todos == nil {
		return "", fmt.Errorf("todos array is required")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	items := make([]TodoItem, 0, len(args.Todos))
	for _, t := range args.Todos {
		if strings.TrimSpace(t.Content) == "" {
			return "", fmt.Errorf("todo item content cannot be empty")
		}
		id := t.ID
		if id == "" {
			id = fmt.Sprintf("todo-%d", len(items)+1)
		}
		status := TodoStatus(t.Status)
		switch status {
		case TodoPending, TodoInProgress, TodoDone:
		default:
			status = TodoPending
		}
		priority := TodoPriority(t.Priority)
		switch priority {
		case TodoHigh, TodoMedium, TodoLow:
		default:
			priority = TodoMedium
		}
		items = append(items, TodoItem{
			ID:        id,
			Content:   t.Content,
			Status:    status,
			Priority:  priority,
			CreatedAt: now,
		})
	}

	store := r.lazyTodos()
	store.mu.Lock()
	store.items = items
	store.mu.Unlock()

	r.saveTodos(items)

	return fmt.Sprintf("Wrote %d todo items", len(items)), nil
}

// handleTodoRead implements todo_read (T-R1P-012).
// Returns the current todo list as formatted text.
func (r *Registry) handleTodoRead(input json.RawMessage) (string, error) {
	var args struct {
		Status string `json:"status"` // optional filter: pending|in_progress|done
	}
	_ = json.Unmarshal(input, &args)

	store := r.lazyTodos()
	store.mu.RLock()
	items := make([]TodoItem, len(store.items))
	copy(items, store.items)
	store.mu.RUnlock()

	if len(items) == 0 {
		return "(no todos)", nil
	}

	// Optional status filter.
	if args.Status != "" {
		filterStatus := TodoStatus(args.Status)
		var filtered []TodoItem
		for _, item := range items {
			if item.Status == filterStatus {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}

	if len(items) == 0 {
		return fmt.Sprintf("(no todos with status %q)", args.Status), nil
	}

	// Render: group by status for readability.
	statusOrder := []TodoStatus{TodoInProgress, TodoPending, TodoDone}
	statusLabel := map[TodoStatus]string{
		TodoInProgress: "IN PROGRESS",
		TodoPending:    "PENDING",
		TodoDone:       "DONE",
	}
	byStatus := make(map[TodoStatus][]TodoItem)
	for _, item := range items {
		byStatus[item.Status] = append(byStatus[item.Status], item)
	}

	var sb strings.Builder
	for _, s := range statusOrder {
		group := byStatus[s]
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "\n## %s\n", statusLabel[s])
		for _, item := range group {
			icon := priorityIcon(item.Priority)
			fmt.Fprintf(&sb, "  [%s] %s %s\n", item.ID, icon, item.Content)
		}
	}
	return strings.TrimLeft(sb.String(), "\n"), nil
}

// handleTodoUpdate implements todo_update (T-R1P-012).
// Patches a single item's status and/or priority by ID.
func (r *Registry) handleTodoUpdate(input json.RawMessage) (string, error) {
	var args struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Priority string `json:"priority"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if args.ID == "" {
		return "", fmt.Errorf("id is required")
	}

	store := r.lazyTodos()
	store.mu.Lock()
	defer store.mu.Unlock()

	for i := range store.items {
		if store.items[i].ID == args.ID {
			if args.Status != "" {
				s := TodoStatus(args.Status)
				switch s {
				case TodoPending, TodoInProgress, TodoDone:
					store.items[i].Status = s
				default:
					return "", fmt.Errorf("invalid status %q: must be pending|in_progress|done", args.Status)
				}
			}
			if args.Priority != "" {
				p := TodoPriority(args.Priority)
				switch p {
				case TodoHigh, TodoMedium, TodoLow:
					store.items[i].Priority = p
				default:
					return "", fmt.Errorf("invalid priority %q: must be high|medium|low", args.Priority)
				}
			}
			snapshot := make([]TodoItem, len(store.items))
			copy(snapshot, store.items)
			go r.saveTodos(snapshot)
			return fmt.Sprintf("Updated todo %q: status=%s priority=%s",
				args.ID, store.items[i].Status, store.items[i].Priority), nil
		}
	}
	return "", fmt.Errorf("todo with id %q not found", args.ID)
}

func priorityIcon(p TodoPriority) string {
	switch p {
	case TodoHigh:
		return "(!)"
	case TodoLow:
		return "( )"
	default:
		return "(-)"
	}
}
