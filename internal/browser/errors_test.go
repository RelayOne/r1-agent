package browser

import (
	"errors"
	"strings"
	"testing"
)

func TestErrors_AsRoundtrip(t *testing.T) {
	t.Parallel()
	cause := errors.New("underlying")

	// ElementNotFound
	var enf *ErrElementNotFound
	err1 := &ErrElementNotFound{Selector: "#missing", Cause: cause}
	if !errors.As(err1, &enf) || enf.Selector != "#missing" {
		t.Errorf("ErrElementNotFound.As failed: %+v", enf)
	}
	if !errors.Is(err1, cause) {
		t.Errorf("ErrElementNotFound.Is(cause) should be true")
	}
	if !strings.Contains(err1.Error(), "#missing") {
		t.Errorf("Error() missing selector: %q", err1.Error())
	}

	// NavigationFailed
	var enav *ErrNavigationFailed
	err2 := &ErrNavigationFailed{URL: "https://bad", Cause: cause}
	if !errors.As(err2, &enav) || enav.URL != "https://bad" {
		t.Errorf("ErrNavigationFailed.As failed")
	}
	if !errors.Is(err2, cause) {
		t.Errorf("ErrNavigationFailed.Is(cause) should be true")
	}

	// ActionTimeout
	var eto *ErrActionTimeout
	err3 := &ErrActionTimeout{Kind: "wait_for_selector", Selector: ".x", Cause: cause}
	if !errors.As(err3, &eto) || eto.Kind != "wait_for_selector" {
		t.Errorf("ErrActionTimeout.As failed")
	}
	if !strings.Contains(err3.Error(), ".x") {
		t.Errorf("Error() missing selector: %q", err3.Error())
	}

	// ChromeLaunchFailed
	var elaunch *ErrChromeLaunchFailed
	err4 := &ErrChromeLaunchFailed{Cause: cause}
	if !errors.As(err4, &elaunch) {
		t.Errorf("ErrChromeLaunchFailed.As failed")
	}

	// InteractiveUnsupported
	var eun *ErrInteractiveUnsupported
	err5 := &ErrInteractiveUnsupported{Kind: ActionClick}
	if !errors.As(err5, &eun) || eun.Kind != ActionClick {
		t.Errorf("ErrInteractiveUnsupported.As failed")
	}
	if !strings.Contains(err5.Error(), "stoke_rod") {
		t.Errorf("InteractiveUnsupported should mention build tag: %q", err5.Error())
	}
}

func TestIsTransient(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"launch", &ErrChromeLaunchFailed{}, true},
		{"nav", &ErrNavigationFailed{URL: "x"}, true},
		{"timeout_wait", &ErrActionTimeout{Kind: string(ActionWaitForSelector)}, true},
		{"timeout_nav", &ErrActionTimeout{Kind: string(ActionNavigate)}, true},
		{"timeout_extract", &ErrActionTimeout{Kind: string(ActionExtractText)}, false},
		{"enf", &ErrElementNotFound{Selector: "#x"}, false},
		{"unsupported", &ErrInteractiveUnsupported{Kind: ActionClick}, false},
		{"plain", errors.New("other"), false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsTransient(tc.err); got != tc.want {
				t.Errorf("IsTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestErrorsNilUnwrapSafe(t *testing.T) {
	t.Parallel()
	// Each typed error should render without a nil-deref when Cause
	// is nil — construction from the rod backend doesn't always
	// wrap a concrete cause.
	e1 := (&ErrElementNotFound{Selector: "x"}).Error()
	e2 := (&ErrNavigationFailed{URL: "u"}).Error()
	e3 := (&ErrActionTimeout{Kind: "click"}).Error()
	e4 := (&ErrChromeLaunchFailed{}).Error()
	for i, s := range []string{e1, e2, e3, e4} {
		if s == "" {
			t.Errorf("empty error string at index %d", i)
		}
	}
}
