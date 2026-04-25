// Package logging provides structured, leveled logging for all Stoke components.
// It wraps the standard library log/slog package with Stoke-specific conventions:
// component tagging, cost tracking, and task-scoped context.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/RelayOne/r1-agent/internal/r1env"
	"github.com/RelayOne/r1-agent/internal/redact"
)

// Level constants matching slog levels for convenience.
const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// contextKey is the key type for storing loggers in context.
type contextKey struct{}

var (
	globalLogger *slog.Logger
	initOnce     sync.Once
)

// Init initializes the global logger. Safe to call multiple times (idempotent).
// level is parsed from string: "debug", "info", "warn", "error".
// output is where logs are written (os.Stderr if nil).
func Init(level string, output io.Writer) {
	initOnce.Do(func() {
		if output == nil {
			output = os.Stderr
		}
		// Wrap every log sink with the secret redactor. This is the
		// centralized egress control: whatever path a secret arrives on
		// (tool output, API error, operator shell command echoed into a
		// log line), it gets stripped before it lands on disk. Operators
		// who want raw logs for debugging can set STOKE_LOG_REDACT=0.
		if r1env.Get("R1_LOG_REDACT", "STOKE_LOG_REDACT") != "0" {
			output = redact.NewWriter(output)
		}
		var lvl slog.Level
		switch strings.ToLower(level) {
		case "debug":
			lvl = slog.LevelDebug
		case "warn", "warning":
			lvl = slog.LevelWarn
		case "error":
			lvl = slog.LevelError
		default:
			lvl = slog.LevelInfo
		}
		handler := slog.NewJSONHandler(output, &slog.HandlerOptions{
			Level: lvl,
		})
		globalLogger = slog.New(handler)
	})
}

// Global returns the package-level logger. Initializes with defaults if Init was never called.
func Global() *slog.Logger {
	if globalLogger == nil {
		Init("info", nil)
	}
	return globalLogger
}

// With returns a new logger with the given key-value attributes.
func With(args ...any) *slog.Logger {
	return Global().With(args...)
}

// Component returns a logger tagged with a component name.
// Usage: logging.Component("workflow").Info("task started", "task_id", id)
func Component(name string) *slog.Logger {
	return Global().With("component", name)
}

// Task returns a logger tagged with both component and task ID.
func Task(component, taskID string) *slog.Logger {
	return Global().With("component", component, "task_id", taskID)
}

// WithContext returns a new context carrying the given logger.
func WithContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

// FromContext extracts the logger from the context, falling back to the global logger.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok {
		return l
	}
	return Global()
}

// Cost logs a cost event for tracking spend across tasks.
func Cost(logger *slog.Logger, taskID string, costUSD float64, model string) {
	logger.Info("cost",
		"task_id", taskID,
		"cost_usd", costUSD,
		"model", model,
	)
}

// Attempt logs a task attempt with its outcome.
func Attempt(logger *slog.Logger, taskID string, number int, success bool, durationMs int64) {
	logger.Info("attempt",
		"task_id", taskID,
		"attempt", number,
		"success", success,
		"duration_ms", durationMs,
	)
}
