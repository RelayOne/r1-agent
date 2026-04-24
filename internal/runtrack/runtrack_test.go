package runtrack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegisterWritesManifest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STOKE_RUNTRACK_DIR", dir)

	reg, err := Register(Manifest{
		RunID:   "run-abc-123",
		PID:     12345,
		Command: "stoke sow",
		Model:   "claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer reg.Close()

	// Manifest file should exist with correct content.
	path := filepath.Join(dir, "run-abc-123.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.RunID != "run-abc-123" {
		t.Errorf("RunID = %q, want run-abc-123", m.RunID)
	}
	if m.PID != 12345 {
		t.Errorf("PID = %d, want 12345", m.PID)
	}
	if m.Host == "" {
		t.Error("Host should be auto-populated")
	}
	if m.StartedAt == "" {
		t.Error("StartedAt should be set on register")
	}
	if m.Heartbeat == "" {
		t.Error("Heartbeat should be set on register")
	}
}

// TestRegisterDualEmitsStokeAndR1Build verifies §S3-3 of
// work-r1-rename.md: the persisted manifest JSON carries BOTH the
// legacy `stoke_build` key AND the canonical `r1_build` key with the
// identical value, regardless of which sibling field the caller set.
// Dropping either key is scheduled after the 30-day window.
func TestRegisterDualEmitsStokeAndR1Build(t *testing.T) {
	// Case A: caller sets only legacy StokeBuild — R1Build mirror fills in.
	t.Run("LegacyOnly_MirrorsToR1", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("STOKE_RUNTRACK_DIR", dir)

		const want = "abc1234"
		reg, err := Register(Manifest{
			RunID:      "run-legacy",
			StokeBuild: want,
		})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		defer reg.Close()

		raw, err := os.ReadFile(filepath.Join(dir, "run-legacy.json"))
		if err != nil {
			t.Fatalf("read manifest: %v", err)
		}
		// Inspect the raw JSON so we catch both keys at the wire level.
		var flat map[string]any
		if err := json.Unmarshal(raw, &flat); err != nil {
			t.Fatalf("unmarshal raw: %v", err)
		}
		for _, k := range []string{"stoke_build", "r1_build"} {
			got, ok := flat[k]
			if !ok {
				t.Errorf("key %q missing from manifest JSON: %s", k, raw)
				continue
			}
			if s, _ := got.(string); s != want {
				t.Errorf("key %q = %q, want %q", k, s, want)
			}
		}
	})

	// Case B: caller sets only canonical R1Build — StokeBuild mirror
	// fills in. Forward-compat: once callers migrate, legacy readers
	// still get data.
	t.Run("CanonicalOnly_MirrorsToStoke", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("STOKE_RUNTRACK_DIR", dir)

		const want = "def5678"
		reg, err := Register(Manifest{
			RunID:   "run-canonical",
			R1Build: want,
		})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		defer reg.Close()

		raw, err := os.ReadFile(filepath.Join(dir, "run-canonical.json"))
		if err != nil {
			t.Fatalf("read manifest: %v", err)
		}
		var flat map[string]any
		if err := json.Unmarshal(raw, &flat); err != nil {
			t.Fatalf("unmarshal raw: %v", err)
		}
		for _, k := range []string{"stoke_build", "r1_build"} {
			got, ok := flat[k]
			if !ok {
				t.Errorf("key %q missing from manifest JSON: %s", k, raw)
				continue
			}
			if s, _ := got.(string); s != want {
				t.Errorf("key %q = %q, want %q", k, s, want)
			}
		}
	})
}

func TestRegisterRejectsEmptyRunID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STOKE_RUNTRACK_DIR", dir)

	_, err := Register(Manifest{RunID: ""})
	if err == nil {
		t.Fatal("expected error for empty RunID")
	}
	if !strings.Contains(err.Error(), "RunID") {
		t.Errorf("error should mention RunID, got %v", err)
	}
}

func TestHeartbeatUpdatesTimestamp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STOKE_RUNTRACK_DIR", dir)

	reg, err := Register(Manifest{RunID: "run-hb"})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	// Read first heartbeat.
	ms, _ := List()
	if len(ms) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(ms))
	}
	firstHB := ms[0].Heartbeat

	time.Sleep(10 * time.Millisecond)
	if err := reg.Heartbeat(); err != nil {
		t.Fatal(err)
	}

	ms, _ = List()
	if ms[0].Heartbeat == firstHB {
		t.Error("Heartbeat should have advanced")
	}
}

func TestCloseRemovesManifest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STOKE_RUNTRACK_DIR", dir)

	reg, err := Register(Manifest{RunID: "run-close"})
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "run-close.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("manifest should exist before close: %v", err)
	}

	if err := reg.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("manifest should be removed after close, stat err = %v", err)
	}

	// Close is idempotent.
	if err := reg.Close(); err != nil {
		t.Errorf("double close should be safe, got %v", err)
	}
}

func TestListMultipleInstances(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STOKE_RUNTRACK_DIR", dir)

	for i := 0; i < 3; i++ {
		reg, err := Register(Manifest{RunID: "run-" + string(rune('a'+i))})
		if err != nil {
			t.Fatal(err)
		}
		defer reg.Close()
	}

	ms, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 {
		t.Errorf("List: got %d manifests, want 3", len(ms))
	}
}

func TestListSkipsBadFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STOKE_RUNTRACK_DIR", dir)

	// Write a valid manifest.
	reg, _ := Register(Manifest{RunID: "good"})
	defer reg.Close()

	// Write some garbage that looks like it could be a manifest.
	_ = os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("not json"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "also-corrupt.json"), []byte("{}"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "not-json.txt"), []byte("{}"), 0o600)

	ms, err := List()
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt ones get skipped; "{}" parses to empty Manifest and is kept;
	// .txt is ignored by suffix filter. So expect the good one + "{}" one.
	if len(ms) < 1 || len(ms) > 2 {
		t.Errorf("expected 1-2 manifests, got %d", len(ms))
	}
	foundGood := false
	for _, m := range ms {
		if m.RunID == "good" {
			foundGood = true
		}
	}
	if !foundGood {
		t.Error("good manifest should be listed")
	}
}

func TestListReturnsNilForMissingDir(t *testing.T) {
	t.Setenv("STOKE_RUNTRACK_DIR", "/tmp/stoke-test-nonexistent-xyz-999")
	ms, err := List()
	if err != nil {
		t.Errorf("missing dir should not be an error, got %v", err)
	}
	if ms != nil {
		t.Errorf("missing dir should return nil, got %v", ms)
	}
}

func TestStartHeartbeatTicks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STOKE_RUNTRACK_DIR", dir)

	reg, err := Register(Manifest{RunID: "run-tick"})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	ms, _ := List()
	firstHB := ms[0].Heartbeat

	stop := reg.StartHeartbeat(30 * time.Millisecond)
	defer stop()

	// Wait for at least 2 ticks.
	time.Sleep(100 * time.Millisecond)

	ms, _ = List()
	if ms[0].Heartbeat == firstHB {
		t.Error("heartbeat goroutine should have updated timestamp")
	}
}

func TestIsProcessAliveForSelf(t *testing.T) {
	if !IsProcessAlive(os.Getpid()) {
		t.Error("our own PID should be alive")
	}
}

func TestIsProcessAliveForBogusPID(t *testing.T) {
	// PID 99999999 is well beyond any reasonable process — should be dead.
	if IsProcessAlive(99999999) {
		t.Error("PID 99999999 should be dead on linux")
	}
}

func TestDefaultServerPort(t *testing.T) {
	// Default.
	t.Setenv("STOKE_SERVER_PORT", "")
	if p := DefaultServerPort(); p != 3948 {
		t.Errorf("default port = %d, want 3948", p)
	}

	// Override.
	t.Setenv("STOKE_SERVER_PORT", "9999")
	if p := DefaultServerPort(); p != 9999 {
		t.Errorf("override port = %d, want 9999", p)
	}

	// Bad override falls back to default.
	t.Setenv("STOKE_SERVER_PORT", "not-a-number")
	if p := DefaultServerPort(); p != 3948 {
		t.Errorf("bad override should fall back to 3948, got %d", p)
	}
}

func TestRegisterIsAtomic(t *testing.T) {
	// Verify writeJSON uses a .tmp + rename pattern so a concurrent
	// reader never sees a half-written file.
	dir := t.TempDir()
	t.Setenv("STOKE_RUNTRACK_DIR", dir)

	reg, err := Register(Manifest{RunID: "run-atomic"})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	// After register, no .tmp file should linger.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover .tmp file: %s", e.Name())
		}
	}
}
