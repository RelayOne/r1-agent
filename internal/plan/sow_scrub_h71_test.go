package plan

import (
	"strings"
	"testing"
)

func TestScrubCommand_H71_PnpmFilterBinRewrite(t *testing.T) {
	cases := []struct {
		in         string
		wantIn     string
		wantNotIn  string
		wantChange bool
	}{
		{
			in:         "pnpm --filter web vitest run tests/",
			wantIn:     "pnpm --filter web exec vitest",
			wantChange: true,
		},
		{
			in:         "pnpm --filter web tsc --noEmit",
			wantIn:     "pnpm --filter web exec tsc",
			wantChange: true,
		},
		// Already has exec → no change.
		{
			in:         "pnpm --filter web exec vitest",
			wantIn:     "pnpm --filter web exec vitest",
			wantChange: false,
		},
		// pnpm --filter web run lint → leave alone (run is a subcommand).
		{
			in:         "pnpm --filter web run lint",
			wantIn:     "pnpm --filter web run lint",
			wantChange: false,
		},
		// pnpm --filter web install → leave alone.
		{
			in:         "pnpm --filter web install",
			wantIn:     "pnpm --filter web install",
			wantChange: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, changes := scrubCommand(tc.in)
			if tc.wantChange && len(changes) == 0 {
				t.Fatalf("expected change, got none. in=%q out=%q", tc.in, got)
			}
			if !tc.wantChange {
				for _, c := range changes {
					if strings.Contains(c, "H-71") {
						t.Fatalf("expected no H-71 change, got %q for in=%q", c, tc.in)
					}
				}
			}
			if tc.wantIn != "" && !strings.Contains(got, tc.wantIn) {
				t.Fatalf("out %q does not contain %q", got, tc.wantIn)
			}
			if tc.wantNotIn != "" && strings.Contains(got, tc.wantNotIn) {
				t.Fatalf("out %q contains unwanted %q", got, tc.wantNotIn)
			}
		})
	}
}
