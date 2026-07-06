package costtrack

import "testing"

func TestNormalizeModel_SpecificBeforeBroad(t *testing.T) {
	cases := map[string]string{
		"gpt-4o-mini":          "gpt-4o-mini",
		"openai/gpt-4o-mini":   "gpt-4o-mini",
		"gpt-4o":               "gpt-4o",
		"gpt-4o-2024-08-06":    "gpt-4o",
		"o3-mini":              "o3-mini",
		"o3":                   "o3",
		"o3-pro":               "o3",
		"claude-opus-4-20250514": "claude-opus-4",
	}
	for in, want := range cases {
		got, ok := NormalizeModel(in)
		if !ok || got != want {
			t.Errorf("NormalizeModel(%q) = %q,%v; want %q,true", in, got, ok, want)
		}
	}
	// gpt-4o-mini must be far cheaper than gpt-4o (the 16x mispricing bug).
	mini := ComputeCost("gpt-4o-mini", 1_000_000, 200_000, 0, 0)
	full := ComputeCost("gpt-4o", 1_000_000, 200_000, 0, 0)
	if mini >= full {
		t.Errorf("gpt-4o-mini cost %.4f should be well below gpt-4o cost %.4f", mini, full)
	}
}
