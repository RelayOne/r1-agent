package ledger

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteNodeChainFailureLeavesNoOrphan covers audit A021: the chain
// record is the dedup commit point, so it must be written LAST and
// atomically. If the chain write fails, the content blob written first
// must be cleaned up — otherwise a retry (dedup no-op on chain presence)
// could never repair the half-written node.
func TestWriteNodeChainFailureLeavesNoOrphan(t *testing.T) {
	root := t.TempDir()
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	chainDir := filepath.Join(root, "chain")
	contentDir := filepath.Join(root, "content")

	// Force the chain write to fail after the content tier is written, by
	// replacing the chain DIR with a regular file: atomicWriteFile's
	// os.CreateTemp(chainDir, ...) then fails ENOTDIR. Unlike a
	// chmod-read-only dir, ENOTDIR is not bypassed by root
	// (CAP_DAC_OVERRIDE), so the orphan-cleanup path is exercised in CI
	// containers too, which run as root.
	if err := os.RemoveAll(chainDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chainDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.Remove(chainDir)
		os.MkdirAll(chainDir, 0o755)
	}()

	n := Node{ID: "n-orphan", Type: "test", Content: []byte(`{"k":"v"}`)}
	if err := s.WriteNode(n); err == nil {
		t.Fatal("expected chain-write failure")
	}
	if _, err := os.Stat(filepath.Join(contentDir, "n-orphan.json")); !os.IsNotExist(err) {
		t.Fatalf("orphan content blob left behind after failed chain write (err=%v)", err)
	}

	// Recovery: after the failure clears, a retry must fully persist —
	// under the OLD chain-first ordering the dedup guard made retries a
	// silent no-op with the content unrecoverable. Restore chainDir to a
	// real directory (it was replaced with a file to inject the failure).
	os.Remove(chainDir)
	if err := os.MkdirAll(chainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteNode(n); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	got, err := s.ReadNode("n-orphan")
	if err != nil || len(got.Content) == 0 {
		t.Fatalf("retry did not fully persist: err=%v content=%d bytes", err, len(got.Content))
	}
}
