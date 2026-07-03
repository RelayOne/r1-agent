package engine

// transcript_test.go — event-sourced transcript + shadow checkpoint
// coverage. Everything runs offline: scripted providers (no API),
// real git in t.TempDir() (repo precedent: internal/worktree tests).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/provider"
)

func tUser(text string) agentloop.Message {
	return agentloop.Message{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: text}}}
}

func tAssistant(text string) agentloop.Message {
	return agentloop.Message{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: text}}}
}

func mustWriter(t *testing.T, path string) *transcriptWriter {
	t.Helper()
	w, err := newTranscriptWriter(path)
	if err != nil {
		t.Fatalf("newTranscriptWriter: %v", err)
	}
	return w
}

func TestTranscriptWriterRoundTrip(t *testing.T) {
	toolInput := json.RawMessage(`{"path":"a.txt","content":"x"}`)
	cases := []struct {
		name    string
		history []agentloop.Message
		splits  []int // cumulative prefix lengths handed to appendNew, one per simulated turn
	}{
		{
			name:    "text only",
			history: []agentloop.Message{tUser("hi"), tAssistant("hello")},
			splits:  []int{1, 2},
		},
		{
			name: "tool cycle",
			history: []agentloop.Message{
				tUser("write it"),
				{Role: "assistant", Content: []agentloop.ContentBlock{
					{Type: "text", Text: "on it"},
					{Type: "tool_use", ID: "tu_1", Name: "write_file", Input: toolInput},
				}},
				{Role: "user", Content: []agentloop.ContentBlock{
					{Type: "tool_result", ToolUseID: "tu_1", Content: "File written"},
				}},
				tAssistant("done"),
			},
			splits: []int{1, 3, 4},
		},
		{
			name: "error tool result with mixed blocks",
			history: []agentloop.Message{
				tUser("try"),
				{Role: "assistant", Content: []agentloop.ContentBlock{
					{Type: "tool_use", ID: "tu_9", Name: "bash", Input: json.RawMessage(`{"command":"false"}`)},
				}},
				{Role: "user", Content: []agentloop.ContentBlock{
					{Type: "tool_result", ToolUseID: "tu_9", Content: "exit 1", IsError: true},
					{Type: "text", Text: "[SUPERVISOR NOTE] check the failure"},
				}},
				tAssistant("it failed"),
			},
			splits: []int{1, 3, 4},
		},
		{
			name: "empty content edge",
			history: []agentloop.Message{
				tUser("x"),
				{Role: "assistant", Content: []agentloop.ContentBlock{}},
			},
			splits: []int{1, 2},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.jsonl")
			w := mustWriter(t, path)
			for turn, n := range c.splits {
				w.appendNew(c.history[:n], turn+1)
			}
			w.end("end_turn")
			w.Close()

			got, err := LoadTranscript(path)
			if err != nil {
				t.Fatalf("LoadTranscript: %v", err)
			}
			if !reflect.DeepEqual(got, c.history) {
				t.Errorf("replayed history diverged:\ngot:  %+v\nwant: %+v", got, c.history)
			}
		})
	}
}

func TestTranscriptCompactionReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	w := mustWriter(t, path)

	full := []agentloop.Message{
		tUser("brief"), tAssistant("a1"), tUser("u2"), tAssistant("a2"), tUser("u3"),
	}
	w.appendNew(full, 1)

	compacted := []agentloop.Message{
		tUser("brief"), tAssistant("[compacted summary]"), tUser("u3"),
	}
	w.compaction(compacted)

	after := append(append([]agentloop.Message(nil), compacted...), tAssistant("a3"), tUser("u4"))
	w.appendNew(after, 2)
	w.Close()

	got, err := LoadTranscript(path)
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	if !reflect.DeepEqual(got, after) {
		t.Errorf("post-compaction replay diverged:\ngot:  %+v\nwant: %+v", got, after)
	}
}

func TestTranscriptToleratesTornTrailingLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	w := mustWriter(t, path)
	history := []agentloop.Message{tUser("hi"), tAssistant("hello")}
	w.appendNew(history, 1)
	w.Close()

	// Simulate a crash mid-append: torn partial JSON with no newline.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"message","turn":2,"mess`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := LoadTranscript(path)
	if err != nil {
		t.Fatalf("LoadTranscript with torn tail: %v", err)
	}
	if !reflect.DeepEqual(got, history) {
		t.Errorf("torn tail corrupted replay: %+v", got)
	}
}

func TestTranscriptFailsClosedOnMidFileCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	lines := []string{
		`{"type":"message","ts":"2026-01-01T00:00:00Z","idx":0,"message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		`{garbage not json`,
		`{"type":"end","ts":"2026-01-01T00:00:01Z","stop":"end_turn"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTranscript(path); err == nil {
		t.Fatal("mid-file corruption must fail closed, got nil error")
	}
}

func TestLoadTranscriptRejectsDanglingToolUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	w := mustWriter(t, path)
	w.appendNew([]agentloop.Message{
		tUser("go"),
		{Role: "assistant", Content: []agentloop.ContentBlock{
			{Type: "tool_use", ID: "tu_1", Name: "bash", Input: json.RawMessage(`{}`)},
		}},
	}, 1)
	w.Close()

	if _, err := LoadTranscript(path); err == nil || !strings.Contains(err.Error(), "dangling tool_use") {
		t.Fatalf("want dangling tool_use error, got %v", err)
	}
}

// TestTranscriptMetaIsDispatchBoundary: a rewound retry appends a second
// dispatch to the same file; replay must yield only the last dispatch.
func TestTranscriptMetaIsDispatchBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")

	w1 := mustWriter(t, path)
	w1.meta(map[string]any{"attempt": 1})
	w1.appendNew([]agentloop.Message{tUser("first"), tAssistant("one")}, 1)
	w1.end("end_turn")
	w1.Close()

	second := []agentloop.Message{tUser("second"), tAssistant("two")}
	w2 := mustWriter(t, path)
	w2.meta(map[string]any{"attempt": 2})
	w2.appendNew(second, 1)
	w2.end("end_turn")
	w2.Close()

	got, err := LoadTranscript(path)
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Errorf("replay should hold only the last dispatch:\ngot:  %+v\nwant: %+v", got, second)
	}
}

func TestTruncateTranscriptToCheckpoint(t *testing.T) {
	toolUse := agentloop.Message{Role: "assistant", Content: []agentloop.ContentBlock{
		{Type: "tool_use", ID: "tu_1", Name: "write_file", Input: json.RawMessage(`{}`)},
	}}
	toolResult := agentloop.Message{Role: "user", Content: []agentloop.ContentBlock{
		{Type: "tool_result", ToolUseID: "tu_1", Content: "ok"},
	}}

	t.Run("drops entries after checkpoint", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "t.jsonl")
		w := mustWriter(t, path)
		w.meta(map[string]any{"attempt": 1})
		w.appendNew([]agentloop.Message{tUser("go")}, 1)
		w.checkpoint(CheckpointRecord{Seq: 1, SHA: "abc", Tool: "write_file"})
		w.appendNew([]agentloop.Message{tUser("go"), toolUse, toolResult, tAssistant("done")}, 2)
		w.end("end_turn")
		w.Close()

		if err := TruncateTranscriptToCheckpoint(path, 1); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		got, err := LoadTranscript(path)
		if err != nil {
			t.Fatalf("LoadTranscript: %v", err)
		}
		want := []agentloop.Message{tUser("go")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("repairs dangling tool_use at the cut", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "t.jsonl")
		w := mustWriter(t, path)
		// Checkpoint written after the assistant tool_use message landed
		// but before its tool_result did (parallel-tool interleaving).
		w.appendNew([]agentloop.Message{tUser("go"), toolUse}, 1)
		w.checkpoint(CheckpointRecord{Seq: 1, SHA: "abc", Tool: "write_file"})
		w.appendNew([]agentloop.Message{tUser("go"), toolUse, toolResult}, 2)
		w.Close()

		if err := TruncateTranscriptToCheckpoint(path, 1); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		got, err := LoadTranscript(path)
		if err != nil {
			t.Fatalf("LoadTranscript after repair: %v", err)
		}
		want := []agentloop.Message{tUser("go")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("pairing repair failed:\ngot:  %+v\nwant: %+v", got, want)
		}
	})

	t.Run("missing seq errors and leaves file intact", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "t.jsonl")
		w := mustWriter(t, path)
		w.appendNew([]agentloop.Message{tUser("go")}, 1)
		w.Close()
		before, _ := os.ReadFile(path)

		if err := TruncateTranscriptToCheckpoint(path, 42); err == nil {
			t.Fatal("want error for missing checkpoint seq")
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Error("file modified despite truncate error")
		}
	})

	t.Run("duplicate seq across dispatches picks the latest", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "t.jsonl")
		w1 := mustWriter(t, path)
		w1.meta(map[string]any{"attempt": 1})
		w1.appendNew([]agentloop.Message{tUser("first")}, 1)
		w1.checkpoint(CheckpointRecord{Seq: 1, SHA: "sha1"})
		w1.end("end_turn")
		w1.Close()

		w2 := mustWriter(t, path)
		w2.meta(map[string]any{"attempt": 2})
		w2.appendNew([]agentloop.Message{tUser("second")}, 1)
		w2.checkpoint(CheckpointRecord{Seq: 1, SHA: "sha2"})
		w2.appendNew([]agentloop.Message{tUser("second"), tAssistant("late")}, 2)
		w2.Close()

		if err := TruncateTranscriptToCheckpoint(path, 1); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		got, err := LoadTranscript(path)
		if err != nil {
			t.Fatalf("LoadTranscript: %v", err)
		}
		want := []agentloop.Message{tUser("second")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("latest-dispatch cut failed:\ngot:  %+v\nwant: %+v", got, want)
		}
	})
}

// --- native runner end-to-end (scripted provider, real git) ---

func newTranscriptGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@t.local"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
		{"commit", "--allow-empty", "-m", "seed"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func writeThenDoneProvider() *fakeMCPProvider {
	return &fakeMCPProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "writing the note"},
					{Type: "tool_use", ID: "tu_1", Name: "write_file",
						Input: map[string]interface{}{"path": "note.txt", "content": "hello transcript"}},
				},
				StopReason: "tool_use",
			},
			{
				Content:    []provider.ResponseContent{{Type: "text", Text: "done"}},
				StopReason: "end_turn",
			},
		},
	}
}

func transcriptSpec(t *testing.T, repo string) RunSpec {
	t.Helper()
	return RunSpec{
		Prompt:            "write a note file",
		WorktreeDir:       repo,
		RuntimeDir:        t.TempDir(),
		Phase:             PhaseSpec{Name: "execute", MaxTurns: 4},
		TranscriptPath:    filepath.Join(t.TempDir(), "transcript.jsonl"),
		ShadowCheckpoints: true,
	}
}

func TestNativeRunnerTranscriptEndToEnd(t *testing.T) {
	repo := newTranscriptGitRepo(t)
	spec := transcriptSpec(t, repo)
	n := NewNativeRunner("", "test-model")

	res, err := runWithProvider(t, n, spec, writeThenDoneProvider())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("run errored: %+v", res)
	}
	if got, rerr := os.ReadFile(filepath.Join(repo, "note.txt")); rerr != nil || string(got) != "hello transcript" {
		t.Fatalf("tool side effect missing: %q (err %v)", got, rerr)
	}

	entries, err := readTranscriptEntries(spec.TranscriptPath)
	if err != nil {
		t.Fatalf("readTranscriptEntries: %v", err)
	}
	if len(entries) == 0 || entries[0].Type != "meta" {
		t.Fatalf("first entry should be meta, got %+v", entries)
	}

	var msgs, checkpoints, ends int
	var ck *CheckpointRecord
	var stop string
	for i := range entries {
		switch entries[i].Type {
		case "message":
			msgs++
		case "checkpoint":
			checkpoints++
			ck = entries[i].Checkpoint
		case "end":
			ends++
			stop = entries[i].Stop
		}
	}
	// user, assistant(tool_use), user(tool_result), final assistant —
	// the last one only exists because of the post-Run flush.
	if msgs != 4 {
		t.Errorf("message entries = %d, want 4", msgs)
	}
	if checkpoints != 1 {
		t.Errorf("checkpoint entries = %d, want 1", checkpoints)
	}
	if ends != 1 || stop != "end_turn" {
		t.Errorf("end entries = %d stop=%q, want 1 end_turn", ends, stop)
	}
	if ck == nil || ck.Tool != "write_file" || ck.Seq != 1 || ck.SHA == "" {
		t.Fatalf("checkpoint record malformed: %+v", ck)
	}
	// The recorded SHA resolves to a commit whose tree carries the write,
	// and the pinning ref points at it.
	show := exec.Command("git", "-C", repo, "show", ck.SHA+":note.txt")
	if out, serr := show.Output(); serr != nil || string(out) != "hello transcript" {
		t.Errorf("checkpoint tree note.txt = %q (err %v)", out, serr)
	}
	if ck.Ref != "" {
		rev := exec.Command("git", "-C", repo, "rev-parse", ck.Ref)
		if out, rerr := rev.Output(); rerr != nil || strings.TrimSpace(string(out)) != ck.SHA {
			t.Errorf("ref %s = %q (err %v), want %s", ck.Ref, strings.TrimSpace(string(out)), rerr, ck.SHA)
		}
	}

	// Full replay: valid pairing, final assistant text present.
	history, err := LoadTranscript(spec.TranscriptPath)
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("replayed history = %d messages, want 4", len(history))
	}
	last := history[len(history)-1]
	if last.Role != "assistant" || len(last.Content) == 0 || last.Content[0].Text != "done" {
		t.Errorf("final assistant message missing from replay: %+v", last)
	}
}

func TestNativeRunnerTranscriptFailOpen(t *testing.T) {
	repo := newTranscriptGitRepo(t)
	spec := transcriptSpec(t, repo)
	spec.TranscriptPath = filepath.Join(t.TempDir(), "no", "such", "dir", "t.jsonl")

	res, err := runWithProvider(t, NewNativeRunner("", "test-model"), spec, writeThenDoneProvider())
	if err != nil || res.IsError {
		t.Fatalf("run must succeed without a transcript (fail-open): err=%v res=%+v", err, res)
	}
	if _, serr := os.Stat(spec.TranscriptPath); !os.IsNotExist(serr) {
		t.Errorf("transcript file unexpectedly present (err %v)", serr)
	}
}

func TestNativeRunnerTranscriptKillSwitch(t *testing.T) {
	t.Setenv(EnvDisableTranscript, "1")
	repo := newTranscriptGitRepo(t)
	spec := transcriptSpec(t, repo)

	res, err := runWithProvider(t, NewNativeRunner("", "test-model"), spec, writeThenDoneProvider())
	if err != nil || res.IsError {
		t.Fatalf("run failed under kill switch: err=%v res=%+v", err, res)
	}
	if _, serr := os.Stat(spec.TranscriptPath); !os.IsNotExist(serr) {
		t.Errorf("kill switch ignored: transcript written (err %v)", serr)
	}
}

func TestNativeRunnerShadowCheckpointKillSwitch(t *testing.T) {
	t.Setenv(EnvDisableShadowCheckpoint, "1")
	repo := newTranscriptGitRepo(t)
	spec := transcriptSpec(t, repo)

	res, err := runWithProvider(t, NewNativeRunner("", "test-model"), spec, writeThenDoneProvider())
	if err != nil || res.IsError {
		t.Fatalf("run failed under kill switch: err=%v res=%+v", err, res)
	}
	entries, err := readTranscriptEntries(spec.TranscriptPath)
	if err != nil {
		t.Fatalf("readTranscriptEntries: %v", err)
	}
	for _, e := range entries {
		if e.Type == "checkpoint" {
			t.Errorf("kill switch ignored: checkpoint entry present: %+v", e)
		}
	}
	refs := exec.Command("git", "-C", repo, "for-each-ref", "--format=%(refname)", "refs/r1-checkpoints/")
	if out, _ := refs.Output(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("kill switch ignored: refs written:\n%s", out)
	}
}

// TestNativeRunnerShadowCheckpointNonGitDir: fail-open when the
// worktree isn't a git repository — the run proceeds, just without
// checkpoints.
func TestNativeRunnerShadowCheckpointNonGitDir(t *testing.T) {
	dir := t.TempDir() // plain dir, no git init
	spec := transcriptSpec(t, dir)
	spec.WorktreeDir = dir

	res, err := runWithProvider(t, NewNativeRunner("", "test-model"), spec, writeThenDoneProvider())
	if err != nil || res.IsError {
		t.Fatalf("run must succeed in a non-git dir: err=%v res=%+v", err, res)
	}
	entries, err := readTranscriptEntries(spec.TranscriptPath)
	if err != nil {
		t.Fatalf("readTranscriptEntries: %v", err)
	}
	for _, e := range entries {
		if e.Type == "checkpoint" {
			t.Errorf("checkpoint taken in non-git dir: %+v", e)
		}
	}
}
