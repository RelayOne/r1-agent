package patchapply

import (
	"os"
	"path/filepath"
	"testing"
)

// TestApply_ModifyThroughSymlinkPreservesLink pins the regression the atomic
// temp+rename fix introduced: modifying a source file that is a symlink must
// write THROUGH to the real target and keep the symlink, not replace the link
// with a fresh regular-file inode (which left the target stale).
func TestApply_ModifyThroughSymlinkPreservesLink(t *testing.T) {
	root := t.TempDir()
	realDir := t.TempDir()
	target := filepath.Join(realDir, "real.txt")
	if err := os.WriteFile(target, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "util.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	diff := "--- a/util.txt\n+++ b/util.txt\n@@ -1,2 +1,2 @@\n line1\n-line2\n+line2-edited\n"
	patch, err := Parse(diff)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	res := Apply(patch, root)
	if len(res.Failed) != 0 {
		t.Fatalf("apply failed: %v / %v", res.Failed, res.Errors)
	}
	// The link must still be a symlink...
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced by a regular file (mode=%v err=%v)", fi.Mode(), err)
	}
	// ...and the REAL target must carry the edit.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "line1\nline2-edited\n" {
		t.Fatalf("target not updated through the link: %q", got)
	}
}
