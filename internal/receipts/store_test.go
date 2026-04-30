package receipts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/replay"
)

func TestSignVerifyAndLoad(t *testing.T) {
	repo := t.TempDir()
	r := New("execution", "implemented replay-safe receipt", []byte("payload"))
	r.TaskID = "task-1"
	r.Evidence = []string{"go test ./..."}
	if err := Sign(&r, "secret", "worker"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !Verify(r, "secret") {
		t.Fatal("Verify(secret)=false")
	}
	if Verify(r, "wrong") {
		t.Fatal("Verify(wrong)=true")
	}
	if err := Append(repo, r); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := Load(repo, Filter{TaskID: "task-1"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Load len=%d want 1", len(got))
	}
	if got[0].ID != r.ID {
		t.Fatalf("receipt id=%q want %q", got[0].ID, r.ID)
	}
}

func TestExport(t *testing.T) {
	r := New("execution", "exported", []byte("body"))
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := Export(path, r); err != nil {
		t.Fatalf("Export: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("export was empty")
	}
}

func TestNewReplayReceipt(t *testing.T) {
	rec := replay.NewRecorder("replay-1", "task-7")
	rec.RecordMessage("user", "implement wave b")
	recording := rec.Finish("success")
	receipt, err := NewReplayReceipt(recording, ".r1/replays/replay-1.json", "replay-backed receipt")
	if err != nil {
		t.Fatalf("NewReplayReceipt: %v", err)
	}
	if receipt.Kind != "replay" {
		t.Fatalf("kind=%q want replay", receipt.Kind)
	}
	if receipt.TaskID != "task-7" {
		t.Fatalf("task=%q want task-7", receipt.TaskID)
	}
}
