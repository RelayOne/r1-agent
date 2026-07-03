package sandbox

import (
	"path/filepath"
	"testing"
)

func TestModeFromEnv(t *testing.T) {
	cases := []struct {
		name   string
		r1     string
		legacy string
		want   string
	}{
		{"unset defaults to auto", "", "", ModeAuto},
		{"r1 off", "off", "", ModeOff},
		{"r1 bwrap", "bwrap", "", ModeBwrap},
		{"legacy honored", "", "landlock", ModeLandlock},
		{"r1 wins over legacy", "docker", "off", ModeDocker},
		{"case and space normalized", "  OFF  ", "", ModeOff},
		{"typo passes through for Select to reject", "bwarp", "", "bwarp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("R1_NATIVE_SANDBOX", tc.r1)
			t.Setenv("STOKE_NATIVE_SANDBOX", tc.legacy)
			if got := ModeFromEnv(); got != tc.want {
				t.Errorf("ModeFromEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEgressFromEnv(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{"unset defaults to allow", "", true},
		{"allow", "allow", true},
		{"true", "true", true},
		{"deny", "deny", false},
		{"false", "false", false},
		{"unknown value fails toward deny", "yes-please", false},
		{"case normalized", "ALLOW", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("R1_NATIVE_SANDBOX_NET", tc.val)
			t.Setenv("STOKE_NATIVE_SANDBOX_NET", "")
			if got := EgressFromEnv(); got != tc.want {
				t.Errorf("EgressFromEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultDenyRead(t *testing.T) {
	home := "/home/u"
	got := DefaultDenyRead(home)
	want := map[string]bool{
		filepath.Join(home, ".ssh"):             true,
		filepath.Join(home, ".aws"):             true,
		filepath.Join(home, ".gnupg"):           true,
		filepath.Join(home, ".netrc"):           true,
		filepath.Join(home, ".config", "gh"):    true,
		filepath.Join(home, ".config", "gcloud"): true,
	}
	found := map[string]bool{}
	for _, p := range got {
		found[p] = true
	}
	for p := range want {
		if !found[p] {
			t.Errorf("DefaultDenyRead missing %s", p)
		}
	}
	if DefaultDenyRead("") != nil {
		t.Error("DefaultDenyRead(\"\") must be nil so empty $HOME cannot mask filesystem roots")
	}
	if DefaultWriteCaches("") != nil {
		t.Error("DefaultWriteCaches(\"\") must be nil")
	}
}
