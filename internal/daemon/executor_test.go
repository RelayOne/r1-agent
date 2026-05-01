package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashExecutorExecute(t *testing.T) {
	exec := NewBashExecutor(BashExecutorConfig{
		Shell:          "/bin/bash",
		OutBase:        filepath.Join(t.TempDir(), "proofs"),
		DefaultTimeout: time.Second,
	})

	tests := []struct {
		name          string
		task          *Task
		cancelAfter   time.Duration
		wantErr       string
		wantProofText string
	}{
		{
			name: "success",
			task: &Task{ID: "bash-success", Title: "success", Prompt: "printf 'hello from bash\\n'", Attempts: 1},
		},
		{
			name:    "failure",
			task:    &Task{ID: "bash-failure", Title: "failure", Prompt: "echo boom >&2; exit 3", Attempts: 1},
			wantErr: "boom",
		},
		{
			name:    "timeout",
			task:    &Task{ID: "bash-timeout", Title: "timeout", Prompt: "sleep 5", Attempts: 1, Meta: map[string]string{"timeout": "100ms"}},
			wantErr: context.DeadlineExceeded.Error(),
		},
		{
			name:        "cancellation",
			task:        &Task{ID: "bash-cancel", Title: "cancel", Prompt: "sleep 5", Attempts: 1},
			cancelAfter: 100 * time.Millisecond,
			wantErr:     context.Canceled.Error(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.cancelAfter > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(context.Background())
				go func() {
					time.Sleep(tc.cancelAfter)
					cancel()
				}()
			}

			res := exec.Execute(ctx, tc.task)
			if tc.wantErr == "" && res.Err != nil {
				t.Fatalf("unexpected error: %v", res.Err)
			}
			if tc.wantErr != "" {
				if res.Err == nil || !strings.Contains(res.Err.Error(), tc.wantErr) {
					t.Fatalf("error = %v want substring %q", res.Err, tc.wantErr)
				}
			}
			assertProofsExist(t, res.ProofsPath)
		})
	}
}

func TestClaudeCodeExecutorExecute(t *testing.T) {
	dir := t.TempDir()
	claudeBin := writeExecutable(t, dir, "claude-stub.sh", `#!/bin/bash
set -euo pipefail
prompt="${!#}"
case "$prompt" in
  *FAIL*)
    echo "claude failed" >&2
    exit 7
    ;;
  *SLOW*)
    sleep 2
    echo "late response"
    ;;
  *)
    echo "assistant: $prompt"
    ;;
esac
`)

	exec := NewClaudeCodeExecutor(ClaudeCodeExecutorConfig{
		Binary:         claudeBin,
		OutBase:        filepath.Join(dir, "proofs"),
		DefaultTimeout: time.Second,
	})

	tests := []struct {
		name        string
		task        *Task
		cancelAfter time.Duration
		wantErr     string
	}{
		{
			name: "success",
			task: &Task{ID: "claude-success", Title: "success", Prompt: "SUCCESS", Attempts: 1},
		},
		{
			name:    "failure",
			task:    &Task{ID: "claude-failure", Title: "failure", Prompt: "FAIL", Attempts: 1},
			wantErr: "claude failed",
		},
		{
			name:    "timeout",
			task:    &Task{ID: "claude-timeout", Title: "timeout", Prompt: "SLOW", Attempts: 1, Meta: map[string]string{"timeout": "100ms"}},
			wantErr: context.DeadlineExceeded.Error(),
		},
		{
			name:        "cancellation",
			task:        &Task{ID: "claude-cancel", Title: "cancel", Prompt: "SLOW", Attempts: 1},
			cancelAfter: 100 * time.Millisecond,
			wantErr:     context.Canceled.Error(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.cancelAfter > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(context.Background())
				go func() {
					time.Sleep(tc.cancelAfter)
					cancel()
				}()
			}

			res := exec.Execute(ctx, tc.task)
			if tc.wantErr == "" && res.Err != nil {
				t.Fatalf("unexpected error: %v", res.Err)
			}
			if tc.wantErr != "" {
				if res.Err == nil || !strings.Contains(res.Err.Error(), tc.wantErr) {
					t.Fatalf("error = %v want substring %q", res.Err, tc.wantErr)
				}
			}
			assertProofsExist(t, res.ProofsPath)
		})
	}
}

func TestCodexExecutorExecute(t *testing.T) {
	dir := t.TempDir()
	codexjobBin := writeExecutable(t, dir, "codexjob-stub.sh", `#!/bin/bash
set -euo pipefail
JOBS_DIR="${JOBS_DIR:?missing JOBS_DIR}"
command="${1:-}"
case "$command" in
  start)
    id="${2:?id}"
    shift 3
    effort=""
    estimate="0"
    while [ $# -gt 1 ]; do
      case "$1" in
        --effort)
          effort="$2"
          shift 2
          ;;
        --estimate-bytes)
          estimate="$2"
          shift 2
          ;;
        *)
          break
          ;;
      esac
    done
    prompt="${1:-}"
    job_dir="$JOBS_DIR/$id"
    mkdir -p "$job_dir"
    echo "$id" >> "$JOBS_DIR/calls.log"
    printf '%s' "$prompt" > "$job_dir/prompt.txt"
    printf '{"id":"%s","status":"running","estimate_bytes":%s,"actual_bytes":null,"delta_pct":null,"underdelivered":false,"exit":null}' "$id" "$estimate" > "$job_dir/state.json"
    (
      case "$prompt" in
        *HANG*)
          while [ ! -f "$job_dir/.killed" ]; do sleep 0.05; done
          printf 'cancelled by test\n' > "$job_dir/stderr.log"
          printf '{"id":"%s","status":"failed","estimate_bytes":%s,"actual_bytes":0,"delta_pct":0,"underdelivered":true,"exit":130}' "$id" "$estimate" > "$job_dir/state.json"
          ;;
        *FAIL*)
          sleep 0.1
          printf 'codex failed\n' > "$job_dir/stderr.log"
          printf 'stub failure\n' > "$job_dir/last-message.txt"
          printf '# failed proof\n' > "$job_dir/PROOFS.md"
          printf '[{"claim":"failed","evidence_type":"file_line","evidence_value":"%s:1","source":"stub"}]' "$job_dir/PROOFS.md" > "$job_dir/proofs.json"
          printf '{"id":"%s","status":"failed","estimate_bytes":%s,"actual_bytes":11,"delta_pct":100,"underdelivered":false,"exit":23}' "$id" "$estimate" > "$job_dir/state.json"
          ;;
        *SLOW*)
          sleep 2
          printf 'slow success\n' > "$job_dir/last-message.txt"
          printf '# slow proof\n' > "$job_dir/PROOFS.md"
          printf '[{"claim":"slow","evidence_type":"file_line","evidence_value":"%s:1","source":"stub"}]' "$job_dir/PROOFS.md" > "$job_dir/proofs.json"
          printf '{"id":"%s","status":"done","estimate_bytes":%s,"actual_bytes":41,"delta_pct":100,"underdelivered":false,"exit":0}' "$id" "$estimate" > "$job_dir/state.json"
          ;;
        *)
          sleep 0.1
          printf 'job complete\n' > "$job_dir/last-message.txt"
          printf '# success proof\n' > "$job_dir/PROOFS.md"
          printf '[{"claim":"success","evidence_type":"file_line","evidence_value":"%s:1","source":"stub"}]' "$job_dir/PROOFS.md" > "$job_dir/proofs.json"
          printf '{"id":"%s","status":"done","estimate_bytes":%s,"actual_bytes":321,"delta_pct":100,"underdelivered":false,"exit":0}' "$id" "$estimate" > "$job_dir/state.json"
          ;;
      esac
    ) >/dev/null 2>&1 < /dev/null &
    echo "$id started"
    ;;
  kill)
    id="${2:?id}"
    touch "$JOBS_DIR/$id/.killed"
    ;;
  *)
    echo "unsupported command: $command" >&2
    exit 2
    ;;
esac
`)

	exec := NewCodexExecutor(CodexExecutorConfig{
		Binary:         codexjobBin,
		JobsDir:        filepath.Join(dir, "jobs"),
		DefaultEffort:  "low",
		PollInterval:   25 * time.Millisecond,
		StartTimeout:   time.Second,
		DefaultTimeout: time.Second,
	})

	tests := []struct {
		name        string
		task        *Task
		cancelAfter time.Duration
		wantErr     string
		wantActual  int64
	}{
		{
			name:       "success",
			task:       &Task{ID: "codex-success", Title: "success", Prompt: "SUCCESS", Runner: "codex", EstimateBytes: 321, Attempts: 1},
			wantActual: 321,
		},
		{
			name:    "failure",
			task:    &Task{ID: "codex-failure", Title: "failure", Prompt: "FAIL", Runner: "codex", EstimateBytes: 11, Attempts: 1},
			wantErr: "stub failure",
		},
		{
			name:    "timeout",
			task:    &Task{ID: "codex-timeout", Title: "timeout", Prompt: "HANG", Runner: "codex", EstimateBytes: 10, Attempts: 1, Meta: map[string]string{"timeout": "100ms"}},
			wantErr: context.DeadlineExceeded.Error(),
		},
		{
			name:        "cancellation",
			task:        &Task{ID: "codex-cancel", Title: "cancel", Prompt: "HANG", Runner: "codex", EstimateBytes: 10, Attempts: 1},
			cancelAfter: 100 * time.Millisecond,
			wantErr:     context.Canceled.Error(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.cancelAfter > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(context.Background())
				go func() {
					time.Sleep(tc.cancelAfter)
					cancel()
				}()
			}

			res := exec.Execute(ctx, tc.task)
			if tc.wantErr == "" && res.Err != nil {
				t.Fatalf("unexpected error: %v", res.Err)
			}
			if tc.wantErr != "" {
				if res.Err == nil || !strings.Contains(res.Err.Error(), tc.wantErr) {
					t.Fatalf("error = %v want substring %q", res.Err, tc.wantErr)
				}
			}
			if tc.wantActual > 0 && res.ActualBytes != tc.wantActual {
				t.Fatalf("actual bytes = %d want %d", res.ActualBytes, tc.wantActual)
			}
			if tc.wantErr == "" {
				assertProofsExist(t, res.ProofsPath)
			}
		})
	}
}

func TestCodexExecutorResumesRecoveredJob(t *testing.T) {
	dir := t.TempDir()
	jobsDir := filepath.Join(dir, "jobs")
	codexjobBin := writeExecutable(t, dir, "codexjob-noop.sh", `#!/bin/bash
set -euo pipefail
JOBS_DIR="${JOBS_DIR:?missing JOBS_DIR}"
echo "${2:-}" >> "$JOBS_DIR/calls.log"
`)

	exec := NewCodexExecutor(CodexExecutorConfig{
		Binary:        codexjobBin,
		JobsDir:       jobsDir,
		DefaultEffort: "low",
		PollInterval:  25 * time.Millisecond,
	})

	task := &Task{
		ID:       "resume-task",
		Title:    "resume",
		Prompt:   "SUCCESS",
		Runner:   "codex",
		Attempts: 2,
		Meta: map[string]string{
			"recovered_from_wal": "true",
		},
		ResumeCheckpoint: "verify proofs",
	}

	jobDir := filepath.Join(jobsDir, "resume-task-attempt-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatalf("mkdir job dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(`{"id":"resume-task-attempt-1","status":"running","estimate_bytes":0,"actual_bytes":null,"delta_pct":null,"underdelivered":false,"exit":null}`), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(jobDir, "PROOFS.md"), []byte("# resumed proof\n"), 0o644)
		_ = os.WriteFile(filepath.Join(jobDir, "proofs.json"), []byte(`[{"claim":"resume","evidence_type":"file_line","evidence_value":"`+filepath.Join(jobDir, "PROOFS.md")+`:1","source":"stub"}]`), 0o644)
		_ = os.WriteFile(filepath.Join(jobDir, "last-message.txt"), []byte("resumed"), 0o644)
		_ = os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(`{"id":"resume-task-attempt-1","status":"done","estimate_bytes":0,"actual_bytes":17,"delta_pct":null,"underdelivered":false,"exit":0}`), 0o644)
	}()

	res := exec.Execute(context.Background(), task)
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.MissionID != "resume-task-attempt-1" {
		t.Fatalf("mission id = %q want resume-task-attempt-1", res.MissionID)
	}
	if _, err := os.Stat(filepath.Join(jobsDir, "calls.log")); !os.IsNotExist(err) {
		t.Fatalf("recovered job should not have started a new codexjob process")
	}
}

func TestDaemonCodexExecutorDispatchesRealJobWrapper(t *testing.T) {
	dir := t.TempDir()
	codexjobBin := writeExecutable(t, dir, "codexjob-stub.sh", `#!/bin/bash
set -euo pipefail
JOBS_DIR="${JOBS_DIR:?missing JOBS_DIR}"
case "${1:-}" in
  start)
    id="${2:?id}"
    job_dir="$JOBS_DIR/$id"
    mkdir -p "$job_dir"
    printf '{"id":"%s","status":"running","estimate_bytes":5,"actual_bytes":null,"delta_pct":null,"underdelivered":false,"exit":null}' "$id" > "$job_dir/state.json"
    (
      sleep 0.1
      printf '# proof\n' > "$job_dir/PROOFS.md"
      printf '[{"claim":"daemon integration","evidence_type":"file_line","evidence_value":"%s:1","source":"stub"}]' "$job_dir/PROOFS.md" > "$job_dir/proofs.json"
      printf 'done\n' > "$job_dir/last-message.txt"
      printf '{"id":"%s","status":"done","estimate_bytes":5,"actual_bytes":55,"delta_pct":100,"underdelivered":false,"exit":0}' "$id" > "$job_dir/state.json"
    ) >/dev/null 2>&1 < /dev/null &
    ;;
  kill)
    exit 0
    ;;
esac
`)

	d, err := New(Config{StateDir: dir, Addr: "127.0.0.1:0", MaxParallel: 1, PollGap: 10}, NewCodexExecutor(CodexExecutorConfig{
		Binary:        codexjobBin,
		JobsDir:       filepath.Join(dir, "jobs"),
		DefaultEffort: "low",
		PollInterval:  25 * time.Millisecond,
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := d.Enqueue(&Task{ID: "codex-daemon", Title: "daemon integration", Prompt: "SUCCESS", Runner: "codex", EstimateBytes: 5}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := pollUntilDone(d, []string{"codex-daemon"}, 3*time.Second); err != nil {
		t.Fatalf("pollUntilDone: %v", err)
	}
	task := d.Queue().Get("codex-daemon")
	if task == nil {
		t.Fatalf("task missing")
	}
	if task.MissionID != "codex-daemon-attempt-1" {
		t.Fatalf("mission id = %q want codex-daemon-attempt-1", task.MissionID)
	}
	assertProofsExist(t, task.ProofsPath)
}

func TestSupportsRunner(t *testing.T) {
	tests := []struct {
		name   string
		exec   Executor
		runner string
		want   bool
	}{
		{name: "noop accepts hybrid", exec: NoopExecutor{}, runner: "hybrid", want: true},
		{name: "bash supports native", exec: NewBashExecutor(BashExecutorConfig{}), runner: "native", want: true},
		{name: "codex rejects claude", exec: NewCodexExecutor(CodexExecutorConfig{}), runner: "claude", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SupportsRunner(tc.exec, tc.runner); got != tc.want {
				t.Fatalf("SupportsRunner(%s, %q) = %v want %v", tc.exec.Type(), tc.runner, got, tc.want)
			}
		})
	}
}

func writeExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func assertProofsExist(t *testing.T, proofsPath string) {
	t.Helper()
	if strings.TrimSpace(proofsPath) == "" {
		t.Fatalf("proofs path empty")
	}
	data, err := os.ReadFile(proofsPath)
	if err != nil {
		t.Fatalf("read proofs %s: %v", proofsPath, err)
	}
	if len(data) == 0 {
		t.Fatalf("proofs file empty: %s", proofsPath)
	}
}

func TestWorkerFailsUnsupportedRunner(t *testing.T) {
	dir := t.TempDir()
	d, err := New(Config{StateDir: dir, Addr: "127.0.0.1:0", MaxParallel: 1, PollGap: 10}, NewCodexExecutor(CodexExecutorConfig{
		Binary:        writeExecutable(t, dir, "codexjob-unused.sh", "#!/bin/bash\nexit 99\n"),
		JobsDir:       filepath.Join(dir, "jobs"),
		DefaultEffort: "low",
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Stop()
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := d.Enqueue(&Task{ID: "bad-runner", Title: "bad", Prompt: "echo hi", Runner: "bash"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task := d.Queue().Get("bad-runner")
		if task != nil && task.State == StateFailed {
			if !strings.Contains(task.Error, `executor "codex" does not support runner "bash"`) {
				t.Fatalf("task error = %q", task.Error)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("task did not fail in time")
}
