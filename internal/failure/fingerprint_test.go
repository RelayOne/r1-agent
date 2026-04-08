package failure

import (
	"testing"
)

func TestFingerprintSameError(t *testing.T) {
	a1 := &Analysis{
		Class:   BuildFailed,
		Summary: "3 error(s)",
		Specifics: []Detail{
			{File: "src/main.ts", Line: 42, Message: "TS2322: Type 'string' is not assignable to type 'number'"},
		},
	}
	a2 := &Analysis{
		Class:   BuildFailed,
		Summary: "1 error(s)",
		Specifics: []Detail{
			{File: "src/main.ts", Line: 87, Message: "TS2322: Type 'string' is not assignable to type 'number'"},
		},
	}

	fp1 := Compute(a1)
	fp2 := Compute(a2)

	if !fp1.Same(fp2) {
		t.Errorf("same error at different lines should fingerprint identically\nfp1: %s\nfp2: %s", fp1.Hash, fp2.Hash)
	}
}

func TestFingerprintDifferentErrors(t *testing.T) {
	a1 := &Analysis{
		Class:   BuildFailed,
		Specifics: []Detail{
			{Message: "TS2322: Type 'string' is not assignable to type 'number'"},
		},
	}
	a2 := &Analysis{
		Class:   BuildFailed,
		Specifics: []Detail{
			{Message: "TS2304: Cannot find name 'foo'"},
		},
	}

	fp1 := Compute(a1)
	fp2 := Compute(a2)

	if fp1.Same(fp2) {
		t.Error("different errors should not have same fingerprint")
	}
}

func TestFingerprintSimilar(t *testing.T) {
	a1 := &Analysis{
		Class:   BuildFailed,
		Specifics: []Detail{
			{Message: "TS2322: Type 'string' is not assignable to type 'number'"},
			{Message: "TS2304: Cannot find name 'foo'"},
		},
	}
	a2 := &Analysis{
		Class:   BuildFailed,
		Specifics: []Detail{
			{Message: "TS2322: Type 'string' is not assignable to type 'number'"},
		},
	}

	fp1 := Compute(a1)
	fp2 := Compute(a2)

	if fp1.Same(fp2) {
		t.Error("different specifics should not be exactly same")
	}
	if !fp1.Similar(fp2) {
		t.Error("overlapping errors should be similar")
	}
}

func TestFingerprintDifferentClasses(t *testing.T) {
	a1 := &Analysis{Class: BuildFailed, Summary: "build failed"}
	a2 := &Analysis{Class: TestsFailed, Summary: "tests failed"}

	fp1 := Compute(a1)
	fp2 := Compute(a2)

	if fp1.Similar(fp2) {
		t.Error("different classes should not be similar")
	}
}

func TestFingerprintNil(t *testing.T) {
	fp := Compute(nil)
	if fp.Hash != "nil" {
		t.Errorf("nil analysis should produce 'nil' hash, got %s", fp.Hash)
	}
}

func TestNormalizeMessage(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"src/main.ts:42:10: error", "src/main.ts error"},
		{"Type error at (42,10)", "Type error at"},
		{"pointer at 0xDEADBEEF is nil", "pointer at <addr> is nil"},
	}
	for _, tc := range tests {
		got := normalizeMessage(tc.in)
		if got != tc.want {
			t.Errorf("normalizeMessage(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMatchHistory(t *testing.T) {
	fp := Compute(&Analysis{
		Class:     BuildFailed,
		Specifics: []Detail{{Message: "missing import"}},
	})

	history := []Fingerprint{
		Compute(&Analysis{Class: TestsFailed, Specifics: []Detail{{Message: "test failed"}}}),
		Compute(&Analysis{Class: BuildFailed, Specifics: []Detail{{Message: "missing import"}}}),
		Compute(&Analysis{Class: BuildFailed, Specifics: []Detail{{Message: "missing import"}}}),
	}

	matched, count := MatchHistory(fp, history)
	if matched == nil {
		t.Fatal("expected match")
	}
	if count != 2 {
		t.Errorf("expected 2 matches, got %d", count)
	}
}

func TestBuildPattern(t *testing.T) {
	a := &Analysis{
		Class: BuildFailed,
		Specifics: []Detail{
			{Message: "cannot find 'foo'"},
			{Message: "cannot find 'foo'"},
			{Message: "type error"},
		},
	}
	fp := Compute(a)
	if fp.Pattern == "" {
		t.Error("expected non-empty pattern")
	}
}
