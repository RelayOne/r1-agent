package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/session"
)

// writeFixture helper: drops a .stoke/r1.session.json under repoRoot
// with the provided fields — matches the shape Stoke writes via
// internal/session.WriteSignature.
func writeFixture(t *testing.T, repoRoot string, sig session.SignatureFile) {
	t.Helper()
	stokeDir := filepath.Join(repoRoot, ".stoke")
	if err := os.MkdirAll(stokeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(stokeDir, "r1.session.json"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(sig); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func quietScanner(t *testing.T, db *DB, cfg ScannerConfig) *Scanner {
	t.Helper()
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewScanner(db, cfg)
}

func TestScannerDiscoversSignature(t *testing.T) {
	db := newTestDB(t)

	codeRoot := t.TempDir()
	repo := filepath.Join(codeRoot, "myproj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	writeFixture(t, repo, session.SignatureFile{
		Version:    "1",
		PID:        os.Getpid(), // the test process — alive, so not crashed
		InstanceID: "r1-scan001",
		StartedAt:  now,
		UpdatedAt:  now,
		RepoRoot:   repo,
		Mode:       "ship",
		Status:     "running",
	})

	sc := quietScanner(t, db, ScannerConfig{
		ScanInterval: 10 * time.Second, // not hit during this test
		TailInterval: 50 * time.Millisecond,
		Roots:        []string{codeRoot},
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sc.Start(ctx)

	// Wait for the one scanOnce that runs immediately on Start.
	var rows []SessionRow
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := db.ListSessions("")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		rows = got
		if len(rows) == 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	sc.Wait()
	if len(rows) != 1 || rows[0].InstanceID != "r1-scan001" {
		t.Fatalf("want 1 row for r1-scan001, got %+v", rows)
	}
}

func TestScannerMarksDeadPIDCrashed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal-0 probe unreliable on Windows CI")
	}
	db := newTestDB(t)

	// Spawn a cheap child and let it exit — its PID is now guaranteed
	// dead for the liveness probe.
	cmd := exec.Command("/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	deadPID := cmd.ProcessState.Pid()
	// Give the OS a moment to reap and recycle before probing.
	time.Sleep(20 * time.Millisecond)

	sig := session.SignatureFile{
		InstanceID: "r1-dead",
		PID:        deadPID,
		Status:     "running",
		StartedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		RepoRoot:   t.TempDir(),
	}
	if err := db.UpsertSession(sig); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	sc := quietScanner(t, db, ScannerConfig{
		ScanInterval: time.Hour, // we'll drive liveness directly
		TailInterval: time.Hour,
		Roots:        []string{t.TempDir()},
	})

	sc.liveness(context.Background())

	row, err := db.GetSession("r1-dead")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Status != "crashed" {
		t.Fatalf("status=%q, want crashed", row.Status)
	}
}

func TestScannerTailsStreamFile(t *testing.T) {
	db := newTestDB(t)

	repo := t.TempDir()
	streamPath := filepath.Join(repo, "stream.jsonl")
	if err := os.WriteFile(streamPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	sig := session.SignatureFile{
		InstanceID: "r1-tail001",
		PID:        os.Getpid(),
		Status:     "running",
		StreamFile: streamPath,
		StartedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		RepoRoot:   repo,
	}
	if err := db.UpsertSession(sig); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	sc := quietScanner(t, db, ScannerConfig{
		ScanInterval: time.Hour,
		TailInterval: 25 * time.Millisecond,
		Roots:        []string{t.TempDir()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sc.Start(ctx)
	// Start will call reconcileTailers on its first scan pass, which
	// brings up a tailer for this session.

	// Append two events to the stream file.
	f, err := os.OpenFile(streamPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"stoke.session.start","ts":"2026-04-20T21:00:00Z","data":{}}`,
		`{"type":"stoke.task.start","ts":"2026-04-20T21:00:01Z","data":{}}`,
	}
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	// Poll for the events to land in the DB.
	var events []EventRow
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := db.ListEvents("r1-tail001", 0, 100)
		if err != nil {
			t.Fatalf("list events: %v", err)
		}
		events = got
		if len(events) == 2 {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	cancel()
	sc.Wait()

	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if events[0].EventType != "stoke.session.start" {
		t.Errorf("event[0]=%q", events[0].EventType)
	}
	if events[1].EventType != "stoke.task.start" {
		t.Errorf("event[1]=%q", events[1].EventType)
	}
}

func TestScannerLoadsLedger(t *testing.T) {
	db := newTestDB(t)

	repo := t.TempDir()
	ledgerDir := filepath.Join(repo, ".stoke", "ledger")
	nodesDir := filepath.Join(ledgerDir, "nodes")
	edgesDir := filepath.Join(ledgerDir, "edges")
	if err := os.MkdirAll(nodesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(edgesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	nodeJSON := `{"id":"node-abc","type":"task","mission_id":"m1","created_at":"2026-04-20T21:00:00Z","created_by":"dev"}`
	edgeJSON := `{"id":"e-1","from":"node-abc","to":"node-def","type":"depends_on"}`
	if err := os.WriteFile(filepath.Join(nodesDir, "node-abc.json"), []byte(nodeJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(edgesDir, "e-1.json"), []byte(edgeJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	codeRoot := t.TempDir()
	proj := filepath.Join(codeRoot, "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, proj, session.SignatureFile{
		InstanceID: "r1-led001",
		Status:     "running",
		StartedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		RepoRoot:   proj,
		LedgerDir:  ledgerDir,
	})

	sc := quietScanner(t, db, ScannerConfig{
		ScanInterval: time.Hour,
		TailInterval: time.Hour,
		Roots:        []string{codeRoot},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sc.Start(ctx)

	var snap LedgerSnapshot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, err := db.GetLedger("r1-led001")
		if err != nil {
			t.Fatalf("get ledger: %v", err)
		}
		snap = s
		if len(snap.Nodes) == 1 && len(snap.Edges) == 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	sc.Wait()
	if len(snap.Nodes) != 1 || snap.Nodes[0].ID != "node-abc" {
		t.Errorf("nodes=%+v", snap.Nodes)
	}
	if len(snap.Edges) != 1 || snap.Edges[0].From != "node-abc" {
		t.Errorf("edges=%+v", snap.Edges)
	}
}

func TestScannerSkipsExcludedDirs(t *testing.T) {
	db := newTestDB(t)

	codeRoot := t.TempDir()
	// Drop a signature inside node_modules — scanner MUST NOT descend.
	deep := filepath.Join(codeRoot, "proj", "node_modules", "fake-pkg")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, deep, session.SignatureFile{
		InstanceID: "r1-insidenm",
		Status:     "running",
		StartedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		RepoRoot:   deep,
	})
	// And one that SHOULD be found.
	good := filepath.Join(codeRoot, "proj2")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, good, session.SignatureFile{
		InstanceID: "r1-visible",
		Status:     "running",
		StartedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		RepoRoot:   good,
	})

	sc := quietScanner(t, db, ScannerConfig{
		ScanInterval: time.Hour,
		TailInterval: time.Hour,
		Roots:        []string{codeRoot},
	})
	sc.scanOnce(context.Background())

	rows, err := db.ListSessions("")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 (node_modules suppressed), got %d: %+v", len(rows), rows)
	}
	if rows[0].InstanceID != "r1-visible" {
		t.Errorf("unexpected id: %q", rows[0].InstanceID)
	}
}
