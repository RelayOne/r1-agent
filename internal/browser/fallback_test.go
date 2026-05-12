// fallback_test.go: FallbackProvider behavior + permanent-error
// pass-through tests.

package browser

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// stubProvider is a minimal Provider for fallback tests.
type stubProvider struct {
	name       string
	openErrs   []error // consumed by each Open call
	openCalls  atomic.Int64
	closeCalls atomic.Int64
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Open(ctx context.Context, opts SessionOpts) (Session, error) {
	i := s.openCalls.Add(1) - 1
	if int(i) < len(s.openErrs) {
		if err := s.openErrs[i]; err != nil {
			return nil, err
		}
	}
	return &stubSession{name: s.name + "-sess", id: s.name + "-id"}, nil
}

func (s *stubProvider) Close(ctx context.Context) error {
	s.closeCalls.Add(1)
	return nil
}

type stubSession struct {
	name, id string
}

func (s *stubSession) ID() string                                                       { return s.id }
func (s *stubSession) Navigate(context.Context, string) (NavigateResult, error)         { return NavigateResult{Status: 200}, nil }
func (s *stubSession) WaitFor(context.Context, string, time.Duration) error             { return nil }
func (s *stubSession) Screenshot(context.Context) ([]byte, error)                       { return []byte{0x89, 0x50, 0x4E, 0x47}, nil }
func (s *stubSession) Eval(context.Context, string) (any, error)                        { return nil, nil }
func (s *stubSession) Close(context.Context) error                                      { return nil }

// TestFallback_PrimarySucceeds — secondary never called.
func TestFallback_PrimarySucceeds(t *testing.T) {
	t.Parallel()
	pri := &stubProvider{name: "browserless"}
	sec := &stubProvider{name: "local"}
	f := NewFallback(pri, sec, NewHub())
	_, err := f.Open(context.Background(), SessionOpts{TenantID: "t"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if pri.openCalls.Load() != 1 || sec.openCalls.Load() != 0 {
		t.Errorf("call counts: pri=%d sec=%d", pri.openCalls.Load(), sec.openCalls.Load())
	}
}

// TestFallback_TransientErrorFallsBack — primary errors twice with
// transient, secondary succeeds; assert fallback fires both times,
// then primary recovers (no fallback on third call).
func TestFallback_TransientErrorFallsBack(t *testing.T) {
	t.Parallel()
	pri := &stubProvider{
		name:     "browserless",
		openErrs: []error{ErrProviderUnreachable, ErrProviderUnreachable, nil},
	}
	sec := &stubProvider{name: "local"}
	hub := NewHub()
	var fallbackUsed atomic.Int64
	hub.Subscribe(func(name string, _ EventAttrs) {
		if name == EvtFallbackUsed {
			fallbackUsed.Add(1)
		}
	})
	f := NewFallback(pri, sec, hub)
	for i := 0; i < 3; i++ {
		_, err := f.Open(context.Background(), SessionOpts{TenantID: "t"})
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
	}
	if fallbackUsed.Load() != 2 {
		t.Errorf("fallback_used count = %d, want 2", fallbackUsed.Load())
	}
	if sec.openCalls.Load() != 2 {
		t.Errorf("secondary open count = %d, want 2", sec.openCalls.Load())
	}
}

// TestFallback_PermanentErrorsPassThrough — auth + token + budget +
// denial never fall back.
func TestFallback_PermanentErrorsPassThrough(t *testing.T) {
	t.Parallel()
	cases := []error{
		ErrAuthFailed,
		ErrNoToken,
		ErrNoTenantToken,
		ErrBudgetExhausted,
		&NetworkDeniedError{Host: "x"},
	}
	for _, want := range cases {
		want := want
		t.Run(want.Error(), func(t *testing.T) {
			t.Parallel()
			pri := &stubProvider{name: "browserless", openErrs: []error{want}}
			sec := &stubProvider{name: "local"}
			f := NewFallback(pri, sec, NewHub())
			_, err := f.Open(context.Background(), SessionOpts{TenantID: "t"})
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			if sec.openCalls.Load() != 0 {
				t.Errorf("secondary should NOT have been called for %v", want)
			}
		})
	}
}

// TestFallback_NoSecondaryReturnsPrimaryError — when fallback is
// unconfigured, the primary's error is the result.
func TestFallback_NoSecondaryReturnsPrimaryError(t *testing.T) {
	t.Parallel()
	pri := &stubProvider{name: "browserless", openErrs: []error{ErrProviderUnreachable}}
	f := NewFallback(pri, nil, NewHub())
	_, err := f.Open(context.Background(), SessionOpts{TenantID: "t"})
	if !errors.Is(err, ErrProviderUnreachable) {
		t.Errorf("want ErrProviderUnreachable, got %v", err)
	}
}

// TestFallback_CloseBoth asserts Close cascades.
func TestFallback_CloseBoth(t *testing.T) {
	t.Parallel()
	pri := &stubProvider{name: "browserless"}
	sec := &stubProvider{name: "local"}
	f := NewFallback(pri, sec, nil)
	if err := f.Close(context.Background()); err != nil {
		t.Errorf("Close: %v", err)
	}
	if pri.closeCalls.Load() != 1 || sec.closeCalls.Load() != 1 {
		t.Errorf("close counts: pri=%d sec=%d", pri.closeCalls.Load(), sec.closeCalls.Load())
	}
}
