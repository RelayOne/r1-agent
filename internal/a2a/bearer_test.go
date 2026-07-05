package a2a

import "testing"

// SECURITY gap #4: the A2A bearer check must be constant-time and reject
// length-mismatched / prefix-matched tokens, closing the timing side channel.
func TestCheckBearer(t *testing.T) {
	const want = "s3cr3t-token-value"
	cases := []struct {
		name   string
		header string
		ok     bool
	}{
		{"exact match", "Bearer " + want, true},
		{"case-insensitive scheme", "bearer " + want, true},
		{"mixed-case scheme", "BeArEr " + want, true},
		{"empty header", "", false},
		{"no scheme", want, false},
		{"wrong scheme", "Basic " + want, false},
		{"wrong token", "Bearer wrong-token-here", false},
		{"prefix of token (length mismatch)", "Bearer s3cr3t", false},
		{"token plus extra (length mismatch)", "Bearer " + want + "x", false},
		{"empty token vs empty want", "Bearer ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := checkBearer(c.header, want); got != c.ok {
				t.Errorf("checkBearer(%q) = %v, want %v", c.header, got, c.ok)
			}
		})
	}
}

// An empty configured token must never authenticate (fail-closed): a client
// sending "Bearer " (empty presented token) must not pass.
func TestCheckBearerEmptyConfigFailsClosed(t *testing.T) {
	if checkBearer("Bearer ", "") {
		t.Error("empty configured token must not authenticate an empty presented token")
	}
	if checkBearer("Bearer anything", "") {
		t.Error("empty configured token must not authenticate any token")
	}
}
