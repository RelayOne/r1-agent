package main

import (
	"strings"
	"testing"
)

// TestStripLanesFlag covers every supported form of the --lanes
// passthrough plus the case where the flag is absent (default off).
//
// Per specs/tui-lanes.md §"Implementation Checklist" item 27:
//
//	Add cmd/r1/main.go --lanes passthrough on r1 chat-interactive
//	only (other commands ignore).
func TestStripLanesFlag(t *testing.T) {
	cases := []struct {
		name        string
		in          []string
		wantArgs    string
		wantEnabled bool
	}{
		{
			name:        "no flag — default off",
			in:          []string{"--repo", "."},
			wantArgs:    "--repo,.",
			wantEnabled: false,
		},
		{
			name:        "double-dash --lanes",
			in:          []string{"--lanes"},
			wantArgs:    "",
			wantEnabled: true,
		},
		{
			name:        "single-dash -lanes",
			in:          []string{"-lanes"},
			wantArgs:    "",
			wantEnabled: true,
		},
		{
			name:        "--lanes=true explicit on",
			in:          []string{"--lanes=true"},
			wantArgs:    "",
			wantEnabled: true,
		},
		{
			name:        "--lanes=false explicit off",
			in:          []string{"--lanes=false"},
			wantArgs:    "",
			wantEnabled: false,
		},
		{
			name:        "--lanes mixed with other flags",
			in:          []string{"--repo", ".", "--lanes", "--mode", "mode1"},
			wantArgs:    "--repo,.,--mode,mode1",
			wantEnabled: true,
		},
		{
			name:        "empty args",
			in:          []string{},
			wantArgs:    "",
			wantEnabled: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotArgs, gotEnabled := stripLanesFlag(c.in)
			gotJoined := strings.Join(gotArgs, ",")
			if gotJoined != c.wantArgs {
				t.Errorf("stripLanesFlag args: got %q want %q", gotJoined, c.wantArgs)
			}
			if gotEnabled != c.wantEnabled {
				t.Errorf("stripLanesFlag enabled: got %v want %v", gotEnabled, c.wantEnabled)
			}
		})
	}
}
