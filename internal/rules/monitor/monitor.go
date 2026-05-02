package monitor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/session"
)

const journalRelPath = "rules/decisions.jsonl"

type Decision struct {
	Timestamp    time.Time `json:"timestamp"`
	SessionID    string    `json:"session_id,omitempty"`
	SessionMode  string    `json:"session_mode,omitempty"`
	RepoRoot     string    `json:"repo_root"`
	TaskID       string    `json:"task_id,omitempty"`
	ToolName     string    `json:"tool_name"`
	Verdict      string    `json:"verdict"`
	Reason       string    `json:"reason,omitempty"`
	RuleHits     int       `json:"rule_hits"`
	Blocked      bool      `json:"blocked"`
	MatchedRules []string  `json:"matched_rules,omitempty"`
}

type Monitor struct {
	repoRoot string
	mu       sync.Mutex
}

func NewRepo(repoRoot string) *Monitor {
	return &Monitor{repoRoot: strings.TrimSpace(repoRoot)}
}

func (m *Monitor) Record(decision Decision) error {
	if m == nil || strings.TrimSpace(m.repoRoot) == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if decision.Timestamp.IsZero() {
		decision.Timestamp = time.Now().UTC()
	}
	decision.RepoRoot = m.repoRoot
	if decision.SessionID == "" || decision.SessionMode == "" {
		decision.SessionID, decision.SessionMode = loadSessionMeta(m.repoRoot)
	}
	line, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	for _, path := range []string{
		filepath.Join(m.repoRoot, r1dir.Canonical, journalRelPath),
		filepath.Join(m.repoRoot, r1dir.Legacy, journalRelPath),
	} {
		if err := appendLine(path, line); err != nil {
			return err
		}
	}
	return nil
}

func (m *Monitor) List(limit int) ([]Decision, error) {
	path := resolvedPath(m.repoRoot)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	decisions := make([]Decision, 0, max(limit, 16))
	for sc.Scan() {
		var decision Decision
		if err := json.Unmarshal(sc.Bytes(), &decision); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		decisions = append(decisions, decision)
		if limit > 0 && len(decisions) > limit {
			copy(decisions, decisions[1:])
			decisions = decisions[:limit]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return decisions, nil
}

func (m *Monitor) Tail(ctx context.Context, w io.Writer, follow bool, last int) error {
	decisions, err := m.List(last)
	if err != nil {
		if os.IsNotExist(err) && !follow {
			return nil
		}
		if os.IsNotExist(err) && follow {
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(250 * time.Millisecond):
				}
				if _, statErr := os.Stat(resolvedPath(m.repoRoot)); statErr == nil {
					return m.Tail(ctx, w, follow, last)
				}
			}
		}
		return err
	}
	for _, decision := range decisions {
		if err := writeDecision(w, decision); err != nil {
			return err
		}
	}
	if !follow {
		return nil
	}

	path := resolvedPath(m.repoRoot)
	offset, err := fileSize(path)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		nextOffset, err := streamFromOffset(path, offset, w)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		offset = nextOffset
	}
}

func resolvedPath(repoRoot string) string {
	canonical := filepath.Join(repoRoot, r1dir.Canonical, journalRelPath)
	if _, err := os.Stat(canonical); err == nil {
		return canonical
	}
	legacy := filepath.Join(repoRoot, r1dir.Legacy, journalRelPath)
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return canonical
}

func appendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

func loadSessionMeta(repoRoot string) (string, string) {
	for _, path := range []string{
		filepath.Join(repoRoot, r1dir.Canonical, "r1.session.json"),
		filepath.Join(repoRoot, r1dir.Legacy, "r1.session.json"),
	} {
		sig, err := session.LoadSignature(path)
		if err == nil {
			return sig.InstanceID, sig.Mode
		}
	}
	return "", ""
}

func writeDecision(w io.Writer, decision Decision) error {
	_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
		decision.Timestamp.UTC().Format(time.RFC3339),
		fallback(decision.SessionID, "-"),
		decision.Verdict,
		decision.ToolName,
		fallback(decision.Reason, "-"),
	)
	return err
}

func fallback(value, defaultValue string) string {
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return value
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func streamFromOffset(path string, offset int64, w io.Writer) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var decision Decision
		if err := json.Unmarshal(sc.Bytes(), &decision); err != nil {
			return offset, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := writeDecision(w, decision); err != nil {
			return offset, err
		}
	}
	if err := sc.Err(); err != nil {
		return offset, err
	}
	nextOffset, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return offset, err
	}
	return nextOffset, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
