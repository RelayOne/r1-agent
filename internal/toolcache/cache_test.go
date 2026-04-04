package toolcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPutAndGet(t *testing.T) {
	c := New(DefaultConfig())

	c.Put("grep", "pattern=foo", "match at line 10", 50)

	result, ok := c.Get("grep", "pattern=foo")
	if !ok || result != "match at line 10" {
		t.Errorf("expected cached result, got %q ok=%v", result, ok)
	}
}

func TestMiss(t *testing.T) {
	c := New(DefaultConfig())

	_, ok := c.Get("grep", "pattern=bar")
	if ok {
		t.Error("should miss on empty cache")
	}
}

func TestTTLExpiry(t *testing.T) {
	c := New(Config{MaxEntries: 100, TTL: 1 * time.Millisecond})

	c.Put("read", "file.go", "content", 100)
	time.Sleep(5 * time.Millisecond)

	_, ok := c.Get("read", "file.go")
	if ok {
		t.Error("should expire after TTL")
	}
}

func TestLRUEviction(t *testing.T) {
	c := New(Config{MaxEntries: 2, TTL: time.Hour})

	c.Put("t1", "a", "r1", 10)
	c.Put("t2", "b", "r2", 10)
	c.Put("t3", "c", "r3", 10) // should evict t1

	_, ok := c.Get("t1", "a")
	if ok {
		t.Error("t1 should be evicted")
	}

	_, ok = c.Get("t2", "b")
	if !ok {
		t.Error("t2 should still be cached")
	}
}

func TestInvalidate(t *testing.T) {
	c := New(DefaultConfig())
	c.Put("grep", "pattern", "result", 50)

	c.Invalidate("grep", "pattern")

	_, ok := c.Get("grep", "pattern")
	if ok {
		t.Error("should be invalidated")
	}
}

func TestInvalidateFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.go")
	os.WriteFile(fp, []byte("content"), 0644)

	c := New(DefaultConfig())
	c.PutFile("read", fp, fp, "content", 50)

	c.InvalidateFile(fp)

	_, ok := c.Get("read", fp)
	if ok {
		t.Error("file cache should be invalidated")
	}
}

func TestFileMtimeInvalidation(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.go")
	os.WriteFile(fp, []byte("v1"), 0644)

	c := New(DefaultConfig())
	c.PutFile("read", fp, fp, "v1", 50)

	// Modify the file
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(fp, []byte("v2"), 0644)

	_, ok := c.Get("read", fp)
	if ok {
		t.Error("should invalidate on file modification")
	}
}

func TestClear(t *testing.T) {
	c := New(DefaultConfig())
	c.Put("a", "1", "r", 10)
	c.Put("b", "2", "r", 10)

	c.Clear()
	if c.Len() != 0 {
		t.Errorf("expected 0 after clear, got %d", c.Len())
	}
}

func TestStats(t *testing.T) {
	c := New(DefaultConfig())
	c.Put("tool", "args", "result", 100)

	c.Get("tool", "args")    // hit
	c.Get("tool", "missing") // miss

	stats := c.GetStats()
	if stats.Hits != 1 {
		t.Errorf("expected 1 hit, got %d", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("expected 1 miss, got %d", stats.Misses)
	}
	if stats.TokensSaved != 100 {
		t.Errorf("expected 100 tokens saved, got %d", stats.TokensSaved)
	}
}

func TestHitRate(t *testing.T) {
	c := New(DefaultConfig())
	c.Put("t", "a", "r", 10)

	c.Get("t", "a")
	c.Get("t", "a")
	c.Get("t", "miss")

	rate := c.HitRate()
	if rate < 0.6 || rate > 0.7 {
		t.Errorf("expected ~0.66 hit rate, got %f", rate)
	}
}

func TestHitRateEmpty(t *testing.T) {
	c := New(DefaultConfig())
	if c.HitRate() != 0 {
		t.Error("empty cache should have 0 hit rate")
	}
}

func TestTokenBudgetEviction(t *testing.T) {
	c := New(Config{MaxEntries: 100, TTL: time.Hour, MaxTokens: 100})

	c.Put("a", "1", "r", 60)
	c.Put("b", "2", "r", 60) // should evict a to stay under 100

	_, ok := c.Get("a", "1")
	if ok {
		t.Error("a should be evicted for token budget")
	}
}

func TestLRUTouchPromotes(t *testing.T) {
	c := New(Config{MaxEntries: 2, TTL: time.Hour})

	c.Put("a", "1", "r1", 10)
	c.Put("b", "2", "r2", 10)

	// Touch a (makes it most recent)
	c.Get("a", "1")

	// Add c (should evict b, not a)
	c.Put("c", "3", "r3", 10)

	_, ok := c.Get("a", "1")
	if !ok {
		t.Error("a should survive (was recently accessed)")
	}
	_, ok = c.Get("b", "2")
	if ok {
		t.Error("b should be evicted (LRU)")
	}
}
