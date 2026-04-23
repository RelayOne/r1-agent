package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// stubDeployer is a minimal Deployer used to verify registry round
// trips. It records its own name so Get-then-Name asserts factory
// identity rather than just "returned something non-nil".
type stubDeployer struct{ name string }

func (s *stubDeployer) Deploy(context.Context, DeployConfig) (DeployResult, error) {
	return DeployResult{}, nil
}
func (s *stubDeployer) Verify(context.Context, DeployConfig) (bool, string) { return true, "stub" }
func (s *stubDeployer) Rollback(context.Context, DeployConfig) error        { return nil }
func (s *stubDeployer) Name() string                                        { return s.name }

func stubFactory(name string) Factory {
	return func() Deployer { return &stubDeployer{name: name} }
}

// TestRegistry_RegisterGetRoundtrip verifies that a registered
// factory round-trips through Get and yields a Deployer whose Name()
// matches the registration key.
func TestRegistry_RegisterGetRoundtrip(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)

	Register("roundtrip", stubFactory("roundtrip"))

	d, err := Get("roundtrip")
	if err != nil {
		t.Fatalf("Get(roundtrip) returned error: %v", err)
	}
	if d == nil {
		t.Fatal("Get(roundtrip) returned nil Deployer")
	}
	if got := d.Name(); got != "roundtrip" {
		t.Fatalf("Name() = %q, want %q", got, "roundtrip")
	}
}

// TestRegistry_GetUnknown verifies the unknown-provider error shape.
// The CLI layer grep-matches on this text, so "deploy: unknown
// provider <name>" is load-bearing.
func TestRegistry_GetUnknown(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)

	d, err := Get("nope")
	if err == nil {
		t.Fatal("Get(nope) err = nil, want non-nil")
	}
	if d != nil {
		t.Fatalf("Get(nope) deployer = %v, want nil on error", d)
	}
	wantSubstr := "unknown provider nope"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("Get(nope) error %q does not contain %q", err.Error(), wantSubstr)
	}

	// Also ensure it wraps (or at least shares) the deploy: prefix
	// so callers can distinguish our errors from stdlib ones.
	if !strings.HasPrefix(err.Error(), "deploy:") {
		t.Fatalf("error %q does not start with 'deploy:' prefix", err.Error())
	}

	// Sanity: the returned error is not a sentinel we expose
	// elsewhere (we intentionally construct with errors.New-style
	// formatting so callers key off substring, not Is()).
	if errors.Is(err, ErrFlyctlNotFound) {
		t.Fatal("unknown-provider error should not be ErrFlyctlNotFound")
	}
}

// TestRegistry_RegisterDuplicatePanics verifies duplicate-name
// registrations panic. The panic message is not contractually part
// of the API, but we assert it mentions the name so the operator
// stack trace is actionable.
func TestRegistry_RegisterDuplicatePanics(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)

	Register("dup", stubFactory("dup"))

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register(dup) twice did not panic")
		}
		msg := fmt.Sprintf("%v", r)
		if !strings.Contains(msg, "dup") {
			t.Fatalf("panic %q does not name the duplicate provider", msg)
		}
	}()
	Register("dup", stubFactory("dup"))
}

// TestRegistry_RegisterEmptyNamePanics guards against a silent
// "" registration that would later look like a missing provider.
func TestRegistry_RegisterEmptyNamePanics(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal(`Register("", f) did not panic`)
		}
	}()
	Register("", stubFactory(""))
}

// TestRegistry_RegisterNilFactoryPanics guards against registering a
// nil Factory that would nil-panic on the first Get().
func TestRegistry_RegisterNilFactoryPanics(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register(nilFactory) did not panic")
		}
	}()
	Register("nilf", nil)
}

// TestRegistry_NamesSorted asserts Names returns providers in
// lexicographic order regardless of registration sequence.
func TestRegistry_NamesSorted(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)

	// Register out of order.
	Register("vercel", stubFactory("vercel"))
	Register("cloudflare", stubFactory("cloudflare"))
	Register("fly", stubFactory("fly"))

	got := Names()
	want := []string{"cloudflare", "fly", "vercel"}
	if len(got) != len(want) {
		t.Fatalf("Names() length = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names()[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

// TestRegistry_NamesEmpty covers the zero-state: Names() returns a
// non-nil empty slice so callers can range without nil-checking.
func TestRegistry_NamesEmpty(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)

	got := Names()
	if got == nil {
		t.Fatal("Names() = nil, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("Names() = %v, want []", got)
	}
}

// TestRegistry_GetReturnsFreshInstance confirms each Get call invokes
// the Factory and hands back a separate Deployer. Shared instances
// would allow one deploy's state (e.g. in-flight NDJSON buffer) to
// bleed into another.
func TestRegistry_GetReturnsFreshInstance(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)

	var calls int
	Register("fresh", func() Deployer {
		calls++
		return &stubDeployer{name: "fresh"}
	})

	a, err := Get("fresh")
	if err != nil {
		t.Fatalf("Get a: %v", err)
	}
	b, err := Get("fresh")
	if err != nil {
		t.Fatalf("Get b: %v", err)
	}
	if a == b {
		t.Fatal("Get returned the same Deployer pointer for two calls")
	}
	if calls != 2 {
		t.Fatalf("factory invoked %d times, want 2", calls)
	}
}

// TestRegistry_ConcurrentRegister exercises Register from many
// goroutines. Run under -race to confirm no data races on the
// registry map. Each goroutine registers a unique name so we also
// assert total count is exactly N.
func TestRegistry_ConcurrentRegister(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)

	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("p-%03d", idx)
			Register(name, stubFactory(name))
		}(i)
	}
	wg.Wait()

	if got := len(Names()); got != n {
		t.Fatalf("registered %d, Names() has %d", n, got)
	}

	// Spot-check a random entry round-trips.
	d, err := Get("p-017")
	if err != nil {
		t.Fatalf("Get(p-017): %v", err)
	}
	if d.Name() != "p-017" {
		t.Fatalf("Name() = %q, want p-017", d.Name())
	}
}

// TestRegistry_ConcurrentGetAndRegister exercises the RWMutex path:
// readers call Get while a writer registers a new provider. Run
// under -race.
func TestRegistry_ConcurrentGetAndRegister(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)

	Register("seed", stubFactory("seed"))

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = Get("seed")
					_ = Names()
				}
			}
		}()
	}

	// Writers.
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("w-%02d", idx)
			Register(name, stubFactory(name))
		}(i)
	}

	// Let writers finish, then stop readers.
	// Use a small waitgroup trick: we can't wg.Wait twice; instead
	// close stop after all work is plausibly done via a separate
	// coordination wg for writers only.
	close(stop)
	wg.Wait()

	// 1 seed + 16 writers = 17 total.
	if got := len(Names()); got != 17 {
		t.Fatalf("total providers = %d, want 17 (names=%v)", got, Names())
	}
}
