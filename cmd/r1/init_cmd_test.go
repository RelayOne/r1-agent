package main

import (
	"testing"

	"github.com/RelayOne/r1/internal/wizard"
)

// TestParseInitFlags pins the `r1 init` routing contract (audit A076):
// default → modern RunWizard in ModeAuto, --auto/-a → ModeYes (CI-safe),
// --interactive → legacy question wizard, --research → research pass.
func TestParseInitFlags(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantDir     string
		wantMode    wizard.Mode
		interactive bool
		research    bool
	}{
		{name: "default is ModeAuto in cwd", args: nil, wantDir: "/cwd", wantMode: wizard.ModeAuto},
		{name: "--auto selects ModeYes", args: []string{"--auto"}, wantDir: "/cwd", wantMode: wizard.ModeYes},
		{name: "-a selects ModeYes", args: []string{"-a"}, wantDir: "/cwd", wantMode: wizard.ModeYes},
		{name: "--interactive routes to legacy backend", args: []string{"--interactive"}, wantDir: "/cwd", wantMode: wizard.ModeAuto, interactive: true},
		{name: "-i routes to legacy backend", args: []string{"-i"}, wantDir: "/cwd", wantMode: wizard.ModeAuto, interactive: true},
		{name: "--research enables research pass", args: []string{"--research"}, wantDir: "/cwd", wantMode: wizard.ModeAuto, research: true},
		{name: "positional dir overrides cwd", args: []string{"/proj", "--auto"}, wantDir: "/proj", wantMode: wizard.ModeYes},
		{name: "flags combine", args: []string{"/proj", "--auto", "--research"}, wantDir: "/proj", wantMode: wizard.ModeYes, research: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseInitFlags(tc.args, "/cwd")
			if got.projectDir != tc.wantDir {
				t.Errorf("projectDir = %q, want %q", got.projectDir, tc.wantDir)
			}
			if got.mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", got.mode, tc.wantMode)
			}
			if got.interactive != tc.interactive {
				t.Errorf("interactive = %v, want %v", got.interactive, tc.interactive)
			}
			if got.research != tc.research {
				t.Errorf("research = %v, want %v", got.research, tc.research)
			}
		})
	}
}
