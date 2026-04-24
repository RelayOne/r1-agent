package r1dir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestRootFor_PrefersCanonical covers the canonical-only resolution: when
// `.r1/` exists under the repo, Root() picks it even if `.stoke/` is
// absent.
func TestRootFor_PrefersCanonical(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, Canonical), 0o700); err != nil {
		t.Fatalf("mkdir canonical: %v", err)
	}
	if got := RootFor(repo); got != Canonical {
		t.Errorf("RootFor(repo) = %q, want %q", got, Canonical)
	}
}

// TestRootFor_FallsBackToLegacy covers the case where only `.stoke/`
// exists on disk; Root() returns ".stoke" so legacy sessions keep
// resolving under the helper.
func TestRootFor_FallsBackToLegacy(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, Legacy), 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if got := RootFor(repo); got != Legacy {
		t.Errorf("RootFor(repo) = %q, want %q (legacy-only)", got, Legacy)
	}
}

// TestRootFor_BothExist_PrefersCanonical covers the mixed-state case
// which will be common for the duration of the transition. Canonical
// must win deterministically.
func TestRootFor_BothExist_PrefersCanonical(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, Canonical), 0o700); err != nil {
		t.Fatalf("mkdir canonical: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, Legacy), 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if got := RootFor(repo); got != Canonical {
		t.Errorf("RootFor(repo) = %q, want %q (both exist → canonical wins)", got, Canonical)
	}
}

// TestRootFor_NeitherExists_ReturnsLegacy documents the empty-repo
// default: before any state is written, we return ".stoke" so that any
// caller which happened to skip the helper and write hardcoded `.stoke/`
// lands in the same place the helper would pick on subsequent reads.
// New callers that go through WriteFile will create both dirs anyway,
// so the first write flips future reads to the canonical side.
func TestRootFor_NeitherExists_ReturnsLegacy(t *testing.T) {
	repo := t.TempDir()
	if got := RootFor(repo); got != Legacy {
		t.Errorf("RootFor(empty repo) = %q, want %q", got, Legacy)
	}
}

// TestReadFile_CanonicalOnly covers the canonical-read happy path.
func TestReadFile_CanonicalOnly(t *testing.T) {
	repo := t.TempDir()
	rel := filepath.Join("sessions", "abc.json")
	mustWrite(t, filepath.Join(repo, Canonical, rel), []byte(`{"canonical":true}`))

	got, err := ReadFileFor(repo, rel)
	if err != nil {
		t.Fatalf("ReadFileFor: %v", err)
	}
	if string(got) != `{"canonical":true}` {
		t.Errorf("got %q, want canonical content", string(got))
	}
}

// TestReadFile_LegacyOnly covers the fallback path: only `.stoke/` has
// the file, so the helper must return its content.
func TestReadFile_LegacyOnly(t *testing.T) {
	repo := t.TempDir()
	rel := filepath.Join("ledger", "nodes", "abc.json")
	mustWrite(t, filepath.Join(repo, Legacy, rel), []byte(`{"legacy":true}`))

	got, err := ReadFileFor(repo, rel)
	if err != nil {
		t.Fatalf("ReadFileFor: %v", err)
	}
	if string(got) != `{"legacy":true}` {
		t.Errorf("got %q, want legacy content", string(got))
	}
}

// TestReadFile_BothExist_PrefersCanonical covers the dual-state case:
// both paths have the file and the canonical one must win.
func TestReadFile_BothExist_PrefersCanonical(t *testing.T) {
	repo := t.TempDir()
	rel := "dup.json"
	mustWrite(t, filepath.Join(repo, Canonical, rel), []byte(`"canonical"`))
	mustWrite(t, filepath.Join(repo, Legacy, rel), []byte(`"legacy"`))

	got, err := ReadFileFor(repo, rel)
	if err != nil {
		t.Fatalf("ReadFileFor: %v", err)
	}
	if string(got) != `"canonical"` {
		t.Errorf("got %q, want canonical content (both exist)", string(got))
	}
}

// TestReadFile_NeitherExists_ReturnsNotExist covers the all-absent case:
// the helper must surface an os.ErrNotExist-wrapping error keyed to the
// canonical path so log lines point at the post-rename layout.
func TestReadFile_NeitherExists_ReturnsNotExist(t *testing.T) {
	repo := t.TempDir()
	rel := "missing.json"

	_, err := ReadFileFor(repo, rel)
	if err == nil {
		t.Fatalf("ReadFileFor: expected error, got nil")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFileFor error = %v, want fs.ErrNotExist", err)
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		if !filepathHasDir(pathErr.Path, Canonical) {
			t.Errorf("pathErr.Path = %q, want canonical path (.r1/)", pathErr.Path)
		}
	}
}

// TestReadFile_CanonicalReadError_DoesNotFallThrough confirms that a
// non-ENOENT error on the canonical side is NOT silently swallowed into
// a legacy fallback. This matters because permissions / disk errors
// should fail loudly; otherwise operators diagnose phantom
// "reads-from-legacy" behavior when the real cause is a corrupted
// canonical file.
func TestReadFile_CanonicalReadError_DoesNotFallThrough(t *testing.T) {
	// Skip on non-unix where chmod semantics differ.
	if os.Getuid() == 0 {
		t.Skip("chmod-based error injection is ineffective as root")
	}
	repo := t.TempDir()
	rel := "perm.json"
	canonical := filepath.Join(repo, Canonical, rel)
	mustWrite(t, canonical, []byte(`"canonical"`))
	mustWrite(t, filepath.Join(repo, Legacy, rel), []byte(`"legacy"`))
	// Clobber canonical perms to force a read error.
	if err := os.Chmod(canonical, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(canonical, 0o600) })

	_, err := ReadFileFor(repo, rel)
	if err == nil {
		t.Fatal("expected error on canonical read, got nil")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error %v unexpectedly is fs.ErrNotExist (should be permission-denied)", err)
	}
}

// TestWriteFile_WritesToBoth covers the dual-write: WriteFile must land
// bytes under both `.r1/<rel>` AND `.stoke/<rel>` so external consumers
// that still read the legacy layout keep seeing the latest state.
func TestWriteFile_WritesToBoth(t *testing.T) {
	repo := t.TempDir()
	rel := filepath.Join("bus", "events.log")
	payload := []byte("event-1\nevent-2\n")

	if err := WriteFileFor(repo, rel, payload, 0o600); err != nil {
		t.Fatalf("WriteFileFor: %v", err)
	}
	for _, root := range []string{Canonical, Legacy} {
		got, err := os.ReadFile(filepath.Join(repo, root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", root, err)
		}
		if string(got) != string(payload) {
			t.Errorf("%s content = %q, want %q", root, string(got), string(payload))
		}
	}
}

// TestWriteFile_CreatesParentDirs covers nested relative paths: neither
// `.r1/bus/` nor `.stoke/bus/` exist beforehand; the helper must create
// them so callers don't need to pre-mkdir for every subdirectory.
func TestWriteFile_CreatesParentDirs(t *testing.T) {
	repo := t.TempDir()
	rel := filepath.Join("deeply", "nested", "sub", "file.json")

	if err := WriteFileFor(repo, rel, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFileFor: %v", err)
	}
	for _, root := range []string{Canonical, Legacy} {
		if _, err := os.Stat(filepath.Join(repo, root, rel)); err != nil {
			t.Errorf("%s nested file not created: %v", root, err)
		}
	}
}

// TestWriteFile_RoundTripViaReadFile confirms WriteFile + ReadFile work
// as a pair: after WriteFile, ReadFile resolves to the canonical side
// (since it was just created) and returns the written bytes.
func TestWriteFile_RoundTripViaReadFile(t *testing.T) {
	repo := t.TempDir()
	rel := "round-trip.json"
	payload := []byte(`{"state":"saved"}`)

	if err := WriteFileFor(repo, rel, payload, 0o600); err != nil {
		t.Fatalf("WriteFileFor: %v", err)
	}
	got, err := ReadFileFor(repo, rel)
	if err != nil {
		t.Fatalf("ReadFileFor: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("round trip got %q, want %q", string(got), string(payload))
	}
}

// TestCanonicalPath covers the explicit accessors used by callers that
// want to reference a specific side of the dual layout.
func TestCanonicalAndLegacyPath(t *testing.T) {
	rel := filepath.Join("ledger", "nodes", "abc.json")
	want := filepath.Join(Canonical, rel)
	if got := CanonicalPath(rel); got != want {
		t.Errorf("CanonicalPath = %q, want %q", got, want)
	}
	want = filepath.Join(Legacy, rel)
	if got := LegacyPath(rel); got != want {
		t.Errorf("LegacyPath = %q, want %q", got, want)
	}
	repo := "/tmp/fakerepo"
	want = filepath.Join(repo, Canonical, rel)
	if got := CanonicalPathFor(repo, rel); got != want {
		t.Errorf("CanonicalPathFor = %q, want %q", got, want)
	}
	want = filepath.Join(repo, Legacy, rel)
	if got := LegacyPathFor(repo, rel); got != want {
		t.Errorf("LegacyPathFor = %q, want %q", got, want)
	}
}

// TestJoinFor covers the JoinFor helper: composition of repo + resolved
// root + trailing parts. Exercises both canonical-present and
// legacy-fallback cases.
func TestJoinFor(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, Canonical), 0o700); err != nil {
		t.Fatalf("mkdir canonical: %v", err)
	}
	got := JoinFor(repo, "skills", "deep-interview")
	want := filepath.Join(repo, Canonical, "skills", "deep-interview")
	if got != want {
		t.Errorf("JoinFor canonical = %q, want %q", got, want)
	}

	repo2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo2, Legacy), 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	got = JoinFor(repo2, "skills", "deep-interview")
	want = filepath.Join(repo2, Legacy, "skills", "deep-interview")
	if got != want {
		t.Errorf("JoinFor legacy = %q, want %q", got, want)
	}
}

// mustWrite creates parent dirs and writes content or fails the test.
// Used to seed test fixtures before exercising the helper under test.
func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// filepathHasDir reports whether path contains the literal directory
// component dir. Used by error-path assertions to confirm the helper
// reports the canonical path in os.PathError.Path.
func filepathHasDir(path, dir string) bool {
	for _, part := range filepath.SplitList(path) {
		_ = part
	}
	// Simpler: check the cleaned path for a directory separator + dir.
	sep := string(filepath.Separator)
	return path == dir ||
		pathContains(path, sep+dir+sep) ||
		pathContains(path, dir+sep)
}

func pathContains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
