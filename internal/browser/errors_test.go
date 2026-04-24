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
	var enf *ElementNotFoundError
	err1 := &ElementNotFoundError{Selector: "#missing", Cause: cause}
	if !errors.As(err1, &enf) || enf.Selector != "#missing" {
		t.Errorf("ElementNotFoundError.As failed: %+v", enf)
	}
	if !errors.Is(err1, cause) {
		t.Errorf("ElementNotFoundError.Is(cause) should be true")
	}
	if !strings.Contains(err1.Error(), "#missing") {
		t.Errorf("Error() missing selector: %q", err1.Error())
	}

	// NavigationFailed
	var enav *NavigationFailedError
	err2 := &NavigationFailedError{URL: "https://bad", Cause: cause}
	if !errors.As(err2, &enav) || enav.URL != "https://bad" {
		t.Errorf("NavigationFailedError.As failed")
	}
	if !errors.Is(err2, cause) {
		t.Errorf("NavigationFailedError.Is(cause) should be true")
	}

	// ActionTimeout
	var eto *ActionTimeoutError
	err3 := &ActionTimeoutError{Kind: "wait_for_selector", Selector: ".x", Cause: cause}
	if !errors.As(err3, &eto) || eto.Kind != "wait_for_selector" {
		t.Errorf("ActionTimeoutError.As failed")
	}
	if !strings.Contains(err3.Error(), ".x") {
		t.Errorf("Error() missing selector: %q", err3.Error())
	}

	// ChromeLaunchFailed
	var elaunch *ChromeLaunchFailedError
	err4 := &ChromeLaunchFailedError{Cause: cause}
	if !errors.As(err4, &elaunch) {
		t.Errorf("ChromeLaunchFailedError.As failed")
	}

	// InteractiveUnsupported
	var eun *InteractiveUnsupportedError
	err5 := &InteractiveUnsupportedError{Kind: ActionClick}
	if !errors.As(err5, &eun) || eun.Kind != ActionClick {
		t.Errorf("InteractiveUnsupportedError.As failed")
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
		{"launch", &ChromeLaunchFailedError{}, true},
		{"nav", &NavigationFailedError{URL: "x"}, true},
		{"timeout_wait", &ActionTimeoutError{Kind: string(ActionWaitForSelector)}, true},
		{"timeout_nav", &ActionTimeoutError{Kind: string(ActionNavigate)}, true},
		{"timeout_extract", &ActionTimeoutError{Kind: string(ActionExtractText)}, false},
		{"enf", &ElementNotFoundError{Selector: "#x"}, false},
		{"unsupported", &InteractiveUnsupportedError{Kind: ActionClick}, false},
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
	e1 := (&ElementNotFoundError{Selector: "x"}).Error()
	e2 := (&NavigationFailedError{URL: "u"}).Error()
	e3 := (&ActionTimeoutError{Kind: "click"}).Error()
	e4 := (&ChromeLaunchFailedError{}).Error()
	for i, s := range []string{e1, e2, e3, e4} {
		if s == "" {
			t.Errorf("empty error string at index %d", i)
		}
	}
}
