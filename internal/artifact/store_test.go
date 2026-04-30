package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStore_PutGetRoundtrip(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	content := []byte("hello, world")
	ref, err := s.Put(content)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasPrefix(ref, "sha256:") {
		t.Errorf("ref %q does not start with sha256:", ref)
	}

	got, err := s.Get(ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("Get bytes mismatch: %q vs %q", got, content)
	}
}

func TestStore_PutIsIdempotent(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	content := []byte("idempotent payload")
	ref1, _ := s.Put(content)
	ref2, _ := s.Put(content)
	if ref1 != ref2 {
		t.Errorf("Put twice should return same ref, got %q vs %q", ref1, ref2)
	}
	// Verify only one file exists
	entries, _ := os.ReadDir(s.Root())
	count := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 file, got %d", count)
	}
}

func TestStore_PutFromReader(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	content := []byte(strings.Repeat("X", 4096))
	ref, err := s.PutFromReader(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("PutFromReader: %v", err)
	}
	got, err := s.Get(ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("roundtrip mismatch")
	}

	// Verify the ref matches the content hash
	expected := sha256.Sum256(content)
	want := "sha256:" + hex.EncodeToString(expected[:])
	if ref != want {
		t.Errorf("ref %q != expected %q", ref, want)
	}
}

func TestStore_PutFromReader_Idempotent(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	content := []byte("duplicate stream")
	ref1, err := s.PutFromReader(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("first PutFromReader: %v", err)
	}
	ref2, err := s.PutFromReader(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("second PutFromReader: %v", err)
	}
	if ref1 != ref2 {
		t.Errorf("idempotent stream should return same ref: %q vs %q", ref1, ref2)
	}
}

func TestStore_Get_Missing_ReturnsErrContentMissing(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	missing := "sha256:" + strings.Repeat("a", 64)
	_, err := s.Get(missing)
	if !errors.Is(err, ErrContentMissing) {
		t.Errorf("expected ErrContentMissing, got %v", err)
	}
}

func TestStore_Get_TamperedFileReturnsErrContentTampered(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	ref, _ := s.Put([]byte("original content"))
	hexSum, _ := parseRef(ref)
	target := filepath.Join(s.Root(), hexSum)

	// Tamper: write different bytes to the same path
	if err := os.WriteFile(target, []byte("tampered content"), 0o644); err != nil {
		t.Fatalf("write tampered: %v", err)
	}

	_, err := s.Get(ref)
	if !errors.Is(err, ErrContentTampered) {
		t.Errorf("expected ErrContentTampered, got %v", err)
	}
}

func TestStore_Stat(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	content := []byte("stat me")
	ref, _ := s.Put(content)

	size, exists, err := s.Stat(ref)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !exists {
		t.Error("expected exists=true")
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}

	missing := "sha256:" + strings.Repeat("b", 64)
	_, exists, err = s.Stat(missing)
	if err != nil {
		t.Fatalf("Stat missing: %v", err)
	}
	if exists {
		t.Error("expected exists=false for missing ref")
	}
}

func TestStore_Redact_PreservesIdempotency(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	content := []byte("about to be shredded")
	ref, _ := s.Put(content)

	// First redact: removes file
	if err := s.Redact(ref); err != nil {
		t.Fatalf("Redact: %v", err)
	}
	_, err := s.Get(ref)
	if !errors.Is(err, ErrContentMissing) {
		t.Errorf("expected ErrContentMissing after redact, got %v", err)
	}

	// Second redact on already-redacted is a no-op
	if err := s.Redact(ref); err != nil {
		t.Errorf("redact-when-missing should be no-op, got: %v", err)
	}
}

func TestStore_Redact_LedgerChainIntact(t *testing.T) {
	// Conceptual test: redacting bytes does not affect the parent ledger
	// node's existence. We model this by: putting bytes, recording the
	// ref, redacting, then confirming the ref string is still valid for
	// future Stat() calls (returns exists=false but no error).
	s, _ := NewStore(t.TempDir())
	ref, _ := s.Put([]byte("doomed"))
	_ = s.Redact(ref)
	_, exists, err := s.Stat(ref)
	if err != nil {
		t.Fatalf("Stat after redact: %v", err)
	}
	if exists {
		t.Error("expected exists=false after redact")
	}
}

func TestStore_RejectsInvalidRefs(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	cases := []string{
		"",
		"sha256:",
		"sha256:short",
		"sha256:" + strings.Repeat("z", 64), // non-hex
		"md5:" + strings.Repeat("a", 64),
	}
	for _, ref := range cases {
		t.Run(ref, func(t *testing.T) {
			_, err := s.Get(ref)
			if err == nil {
				t.Errorf("expected error for invalid ref %q", ref)
			}
		})
	}
}

func TestStore_ConcurrentPut(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	const N = 50
	contents := make([][]byte, N)
	for i := 0; i < N; i++ {
		contents[i] = []byte(strings.Repeat("X", i+1))
	}

	var wg sync.WaitGroup
	refs := make([]string, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := s.Put(contents[i])
			if err != nil {
				t.Errorf("Put: %v", err)
				return
			}
			refs[i] = r
		}(i)
	}
	wg.Wait()

	for i, ref := range refs {
		got, err := s.Get(ref)
		if err != nil {
			t.Errorf("Get %d: %v", i, err)
			continue
		}
		if !bytes.Equal(got, contents[i]) {
			t.Errorf("content %d mismatch", i)
		}
	}
}

func TestStore_PutFromReader_ErrorPropagates(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	r := &errorReader{err: errors.New("synthetic read error")}
	_, err := s.PutFromReader(r)
	if err == nil {
		t.Error("expected error from PutFromReader")
	}
	// Verify no leftover temp files
	entries, _ := os.ReadDir(s.Root())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".put-stream-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

type errorReader struct {
	err error
}

func (e *errorReader) Read(p []byte) (int, error) {
	return 0, e.err
}

// ─── parseRef helper ────────────────────────────────────────────────

func TestParseRef(t *testing.T) {
	good := "sha256:" + strings.Repeat("a", 64)
	hexPart, err := parseRef(good)
	if err != nil {
		t.Errorf("parseRef good: %v", err)
	}
	if hexPart != strings.Repeat("a", 64) {
		t.Errorf("parseRef hex mismatch")
	}

	bad := []string{
		"",
		"sha256:abc",
		"foo:" + strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("Z", 64),
		"sha256:" + strings.Repeat("g", 64),
	}
	for _, b := range bad {
		t.Run(b, func(t *testing.T) {
			_, err := parseRef(b)
			if err == nil {
				t.Errorf("parseRef %q should fail", b)
			}
		})
	}
}

// Ensure interface compliance for io.Reader use in PutFromReader tests.
var _ io.Reader = (*errorReader)(nil)
