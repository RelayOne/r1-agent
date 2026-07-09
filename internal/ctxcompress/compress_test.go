package ctxcompress

import (
	"reflect"
	"testing"
)

func items() []Item {
	return []Item{
		{Index: 0, Bytes: 500, Pinned: true, Compressible: false},   // task brief (pinned)
		{Index: 1, Bytes: 300, Compressible: true, ContentKey: "A"}, // old, big
		{Index: 2, Bytes: 50, Compressible: true, ContentKey: "B"},  // small
		{Index: 3, Bytes: 300, Compressible: true, ContentKey: "A"}, // duplicate of #1 (newer)
		{Index: 4, Bytes: 400, Compressible: true, ContentKey: "C"}, // big
		{Index: 5, Bytes: 900, Pinned: true, Compressible: false},   // recent tail (pinned)
	}
}

func TestThresholdMode(t *testing.T) {
	// MaxBytes=0 -> compress every compressible non-pinned item >= MinBytes.
	c := NewPolicyCompressor(Policy{MinBytes: 100})
	got := c.Compress(items())
	want := []int{1, 3, 4} // #2 is below the 100-byte floor; #0/#5 pinned
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("threshold selection = %v, want %v", got, want)
	}
}

func TestRedundancyMode(t *testing.T) {
	// DropRedundant compresses the OLDER duplicate (#1) but keeps the newest (#3).
	c := NewPolicyCompressor(Policy{DropRedundant: true, MinBytes: 100, MaxBytes: 1_000_000})
	got := c.Compress(items())
	found := false
	for _, i := range got {
		if i == 1 {
			found = true
		}
		if i == 3 {
			t.Fatalf("newest duplicate #3 must be kept, not compressed: %v", got)
		}
	}
	if !found {
		t.Fatalf("older duplicate #1 must be compressed: %v", got)
	}
}

func TestBudgetMode(t *testing.T) {
	// Total live bytes = 500+300+50+300+400+900 = 2450. Budget 2000 => must
	// compress oldest-first until under budget. Compressing #1 saves 300-48=252
	// -> 2198; still over. Compress #3 saves 252 -> 1946 <= 2000. Stop.
	c := NewPolicyCompressor(Policy{MaxBytes: 2000, MinBytes: 100})
	got := c.Compress(items())
	if len(got) < 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("budget selection must compress oldest-first (#1,#3...), got %v", got)
	}
	// #0 and #5 (pinned) must never be selected.
	for _, i := range got {
		if i == 0 || i == 5 {
			t.Fatalf("pinned item selected: %v", got)
		}
	}
}

func TestDeterminism(t *testing.T) {
	c := NewPolicyCompressor(Policy{MaxBytes: 1500, MinBytes: 100, DropRedundant: true})
	a := c.Compress(items())
	b := c.Compress(items())
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("compressor must be deterministic: %v != %v", a, b)
	}
}

// fakeCompressor selects nothing — stands in for a swapped-in backend.
type fakeCompressor struct{}

func (fakeCompressor) Compress([]Item) []int { return nil }
func (fakeCompressor) Backend() string       { return "fake-noop" }

func TestSwapProof(t *testing.T) {
	ResetCompressor()
	defer ResetCompressor()

	// A caller that only uses GetCompressor().
	caller := func() (string, int) {
		c := GetCompressor()
		return c.Backend(), len(c.Compress(items()))
	}

	backend, selected := caller()
	if backend != BackendPolicy || selected == 0 {
		t.Fatalf("default: backend=%q selected=%d", backend, selected)
	}

	SetCompressor(fakeCompressor{})
	backend, selected = caller() // identical call site
	if backend != "fake-noop" || selected != 0 {
		t.Fatalf("after swap: backend=%q selected=%d", backend, selected)
	}
}
