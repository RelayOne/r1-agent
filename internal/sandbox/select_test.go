package sandbox

import (
	"strings"
	"testing"
)

// stubLandlockABI overrides the probe seam for the duration of a test so
// Select's auto/landlock behavior is deterministic regardless of host
// kernel or seccomp profile.
func stubLandlockABI(t *testing.T, abi int) {
	t.Helper()
	orig := landlockABIProbe
	landlockABIProbe = func() int { return abi }
	t.Cleanup(func() { landlockABIProbe = orig })
}

func TestSelect(t *testing.T) {
	cases := []struct {
		name        string
		policy      Policy
		emptyPath   bool // strip PATH so bwrap/docker binaries vanish
		landlockABI int
		wantNil     bool
		wantName    string
		wantErr     []string // substrings the error must contain
	}{
		{
			name:    "off returns nil wrapper and nil error",
			policy:  Policy{Mode: ModeOff},
			wantNil: true,
		},
		{
			name:      "explicit bwrap unavailable fails closed",
			policy:    Policy{Mode: ModeBwrap},
			emptyPath: true,
			wantErr:   []string{"bwrap", "unavailable"},
		},
		{
			name:        "explicit landlock unavailable fails closed",
			policy:      Policy{Mode: ModeLandlock, AllowEgress: true},
			landlockABI: 0,
			wantErr:     []string{"landlock", "unavailable"},
		},
		{
			name:        "explicit landlock available",
			policy:      Policy{Mode: ModeLandlock, AllowEgress: true},
			landlockABI: 3,
			wantName:    ModeLandlock,
		},
		{
			name:        "landlock egress deny needs abi 4",
			policy:      Policy{Mode: ModeLandlock, AllowEgress: false},
			landlockABI: 3,
			wantErr:     []string{"egress"},
		},
		{
			name:    "docker without image fails closed",
			policy:  Policy{Mode: ModeDocker},
			wantErr: []string{"image"},
		},
		{
			name:      "docker with image but no binary fails closed",
			policy:    Policy{Mode: ModeDocker, DockerImage: "golang:1.24"},
			emptyPath: true,
			wantErr:   []string{"docker"},
		},
		{
			name:        "auto with nothing available reports both probes",
			policy:      Policy{Mode: ModeAuto, AllowEgress: true},
			emptyPath:   true,
			landlockABI: 0,
			wantErr:     []string{"bwrap", "landlock"},
		},
		{
			name:        "auto falls back to landlock when bwrap missing",
			policy:      Policy{Mode: ModeAuto, AllowEgress: true},
			emptyPath:   true,
			landlockABI: 5,
			wantName:    ModeLandlock,
		},
		{
			name:        "empty mode means auto",
			policy:      Policy{AllowEgress: true},
			emptyPath:   true,
			landlockABI: 5,
			wantName:    ModeLandlock,
		},
		{
			name:    "unknown mode is an error not auto",
			policy:  Policy{Mode: "bwarp"},
			wantErr: []string{"unknown sandbox mode"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.emptyPath {
				t.Setenv("PATH", t.TempDir())
			}
			stubLandlockABI(t, tc.landlockABI)
			w, err := Select(tc.policy)
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatalf("want error containing %v, got wrapper %v", tc.wantErr, w)
				}
				for _, sub := range tc.wantErr {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("error %q missing substring %q", err, sub)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if w != nil {
					t.Fatalf("want nil wrapper, got %v", w.Name())
				}
				return
			}
			if w == nil || w.Name() != tc.wantName {
				t.Fatalf("want wrapper %q, got %v", tc.wantName, w)
			}
		})
	}
}
