package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func artifactDir(base string, t *Task) string {
	if t == nil {
		return filepath.Join(base, "task-unknown", "attempt-1")
	}
	attempt := t.Attempts
	if attempt < 1 {
		attempt = 1
	}
	return filepath.Join(base, sanitizeExecutorID(t.ID), fmt.Sprintf("attempt-%d", attempt))
}

func sanitizeExecutorID(in string) string {
	in = strings.TrimSpace(in)
	if in == "" {
		return "task-unknown"
	}
	var b strings.Builder
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "task-unknown"
	}
	return out
}

func isRecoveredFromWAL(t *Task) bool {
	if t == nil || t.Meta == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(t.Meta["recovered_from_wal"]), "true")
}

func taskTimeout(t *Task, fallback time.Duration) time.Duration {
	if t == nil || t.Meta == nil {
		return fallback
	}
	if raw := strings.TrimSpace(t.Meta["timeout"]); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	if raw := strings.TrimSpace(t.Meta["timeout_seconds"]); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return fallback
}

func writeExecutorFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func writeExecutorJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeExecutorFile(path, data)
}

func proofsActualBytes(proofsPath string, fallback int64) int64 {
	info, err := os.Stat(proofsPath)
	if err != nil || info.Size() <= 0 {
		return fallback
	}
	return info.Size()
}
