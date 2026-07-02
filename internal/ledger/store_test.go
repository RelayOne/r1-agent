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

	// Make the chain dir read-only so the chain write fails after the
	// content tier has been written.
	if err := os.Chmod(chainDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(chainDir, 0o755)

	n := Node{ID: "n-orphan", Type: "test", Content: []byte(`{"k":"v"}`)}
	if err := s.WriteNode(n); err == nil {
		t.Fatal("expected chain-write failure")
	}
	if _, err := os.Stat(filepath.Join(contentDir, "n-orphan.json")); !os.IsNotExist(err) {
		t.Fatalf("orphan content blob left behind after failed chain write (err=%v)", err)
	}

	// Recovery: after the failure clears, a retry must fully persist —
	// under the OLD chain-first ordering the dedup guard made retries a
	// silent no-op with the content unrecoverable.
	os.Chmod(chainDir, 0o755)
	if err := s.WriteNode(n); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	got, err := s.ReadNode("n-orphan")
	if err != nil || len(got.Content) == 0 {
		t.Fatalf("retry did not fully persist: err=%v content=%d bytes", err, len(got.Content))
	}
}
