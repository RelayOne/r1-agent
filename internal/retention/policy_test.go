package retention

import (
	"strings"
	"testing"
)

func TestIsValidDuration(t *testing.T) {
	valid := []Duration{
		WipeAfterSession,
		Retain7Days,
		Retain30Days,
		Retain90Days,
		RetainForever,
	}
	for _, d := range valid {
		if !IsValidDuration(d) {
			t.Errorf("IsValidDuration(%q) = false, want true", d)
		}
	}

	invalid := []Duration{
		"",
		"forever",
		"retain_1_day",
		"WIPE_AFTER_SESSION",
		"retain_30_day",
		"retain_30_days ",
	}
	for _, d := range invalid {
		if IsValidDuration(d) {
			t.Errorf("IsValidDuration(%q) = true, want false", d)
		}
	}
}

func TestDefaults(t *testing.T) {
	p := Defaults()

	cases := []struct {
		name string
		got  Duration
		want Duration
	}{
		{"EphemeralMemories", p.EphemeralMemories, WipeAfterSession},
		{"SessionMemories", p.SessionMemories, Retain30Days},
		{"PersistentMemories", p.PersistentMemories, RetainForever},
		{"PermanentMemories", p.PermanentMemories, RetainForever},
		{"StreamFiles", p.StreamFiles, Retain90Days},
		{"LedgerNodes", p.LedgerNodes, RetainForever},
		{"LedgerContent", p.LedgerContent, RetainForever},
		{"CheckpointFiles", p.CheckpointFiles, Retain30Days},
		{"PromptsAndResponses", p.PromptsAndResponses, RetainForever},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("Defaults().%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	if err := p.Validate(); err != nil {
		t.Fatalf("Defaults() must validate cleanly, got: %v", err)
	}
}

func TestPolicyValidateAcceptsAllValidValues(t *testing.T) {
	// Every field set to a different valid value (skipping the immutable
	// fields, which must be RetainForever).
	p := Policy{
		EphemeralMemories:   WipeAfterSession,
		SessionMemories:     Retain7Days,
		PersistentMemories:  Retain30Days,
		PermanentMemories:   RetainForever,
		StreamFiles:         Retain90Days,
		LedgerNodes:         RetainForever,
		LedgerContent:       WipeAfterSession,
		CheckpointFiles:     Retain7Days,
		PromptsAndResponses: WipeAfterSession,
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestPolicyValidateRejectsUnknownDuration(t *testing.T) {
	p := Defaults()
	p.SessionMemories = "retain_1_day"

	err := p.Validate()
	if err == nil {
		t.Fatal("expected error for unknown duration, got nil")
	}
	want := `retention.session_memories: invalid duration "retain_1_day", must be one of [wipe_after_session retain_7_days retain_30_days retain_90_days retain_forever]`
	if err.Error() != want {
		t.Errorf("error mismatch:\n  got:  %s\n  want: %s", err.Error(), want)
	}
}

func TestPolicyValidateRejectsEmptyDuration(t *testing.T) {
	// An unset field (zero value "") must fail validation. This guards
	// against partially-populated policies sneaking past the parser.
	p := Defaults()
	p.StreamFiles = ""

	err := p.Validate()
	if err == nil {
		t.Fatal("expected error for empty duration, got nil")
	}
	if !strings.Contains(err.Error(), "retention.stream_files") {
		t.Errorf("error should reference stream_files, got: %v", err)
	}
	if !strings.Contains(err.Error(), `invalid duration ""`) {
		t.Errorf("error should quote the empty value, got: %v", err)
	}
}

func TestPolicyValidateImmutablePermanentMemories(t *testing.T) {
	for _, bad := range []Duration{WipeAfterSession, Retain7Days, Retain30Days, Retain90Days} {
		p := Defaults()
		p.PermanentMemories = bad

		err := p.Validate()
		if err == nil {
			t.Fatalf("PermanentMemories=%q: expected error, got nil", bad)
		}
		want := "retention.permanent_memories: must be retain_forever (immutable)"
		if err.Error() != want {
			t.Errorf("PermanentMemories=%q: error mismatch:\n  got:  %s\n  want: %s", bad, err.Error(), want)
		}
	}
}

func TestPolicyValidateImmutableLedgerNodes(t *testing.T) {
	for _, bad := range []Duration{WipeAfterSession, Retain7Days, Retain30Days, Retain90Days} {
		p := Defaults()
		p.LedgerNodes = bad

		err := p.Validate()
		if err == nil {
			t.Fatalf("LedgerNodes=%q: expected error, got nil", bad)
		}
		want := "retention.ledger_nodes: must be retain_forever (immutable)"
		if err.Error() != want {
			t.Errorf("LedgerNodes=%q: error mismatch:\n  got:  %s\n  want: %s", bad, err.Error(), want)
		}
	}
}

func TestPolicyValidateImmutableFieldStillRejectsUnknown(t *testing.T) {
	// An unknown duration on an immutable field should fail with the
	// invalid-duration message, not the immutability message — the value
	// validity check runs first so operators see the broader hint.
	p := Defaults()
	p.PermanentMemories = "retain_1_day"

	err := p.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `invalid duration "retain_1_day"`) {
		t.Errorf("expected invalid-duration error, got: %v", err)
	}
}
