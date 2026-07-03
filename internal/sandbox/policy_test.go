package sandbox

import (
	"os"
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

func TestEngageFromEnv(t *testing.T) {
	cases := []struct {
		name        string
		r1          string
		wantMode    string
		wantEngaged bool
	}{
		{"unset does not engage (opt-in)", "", ModeOff, false},
		{"off does not engage", "off", ModeOff, false},
		{"on aliases auto and engages", "on", ModeAuto, true},
		{"explicit bwrap engages", "bwrap", ModeBwrap, true},
		{"explicit landlock engages", "landlock", ModeLandlock, true},
		{"explicit docker engages", "docker", ModeDocker, true},
		{"case and space normalized", "  ON  ", ModeAuto, true},
		{"typo engages and passes through for Select to reject", "bwarp", "bwarp", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("R1_NATIVE_SANDBOX", tc.r1)
			t.Setenv("STOKE_NATIVE_SANDBOX", "")
			mode, engaged := EngageFromEnv()
			if mode != tc.wantMode || engaged != tc.wantEngaged {
				t.Errorf("EngageFromEnv() = (%q, %v), want (%q, %v)", mode, engaged, tc.wantMode, tc.wantEngaged)
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
		// Daemon control sockets are masked regardless of home.
		"/run/docker.sock":     true,
		"/var/run/docker.sock": true,
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
	// Empty $HOME still masks the absolute daemon sockets (home-independent),
	// but must NOT emit any home-relative entry that could expand to a fs
	// root. Every returned path must be one of the fixed daemon sockets.
	socketSet := map[string]bool{}
	for _, s := range DefaultDenySockets() {
		socketSet[s] = true
	}
	emptyHome := DefaultDenyRead("")
	if len(emptyHome) == 0 {
		t.Fatal("DefaultDenyRead(\"\") must still mask daemon sockets")
	}
	for _, p := range emptyHome {
		if !socketSet[p] {
			t.Errorf("DefaultDenyRead(\"\") leaked a non-socket path %q (empty $HOME must not expand home-relative entries)", p)
		}
	}
	if !socketSet["/run/docker.sock"] {
		t.Error("DefaultDenySockets must include /run/docker.sock")
	}
	if DefaultWriteCaches("") != nil {
		t.Error("DefaultWriteCaches(\"\") must be nil")
	}
}

func TestWorktreeGitDirs(t *testing.T) {
	t.Run("normal checkout returns nil", func(t *testing.T) {
		work := t.TempDir()
		if err := os.MkdirAll(filepath.Join(work, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := WorktreeGitDirs(work); got != nil {
			t.Errorf("normal checkout: want nil, got %v", got)
		}
	})

	t.Run("no .git returns nil", func(t *testing.T) {
		if got := WorktreeGitDirs(t.TempDir()); got != nil {
			t.Errorf("want nil, got %v", got)
		}
		if got := WorktreeGitDirs(""); got != nil {
			t.Errorf("empty workDir: want nil, got %v", got)
		}
	})

	t.Run("linked worktree resolves gitdir and common dir", func(t *testing.T) {
		parent := t.TempDir()
		commonDir := filepath.Join(parent, ".git")
		gitDir := filepath.Join(commonDir, "worktrees", "wt1")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Authoritative commondir file points back at the parent .git.
		if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		work := t.TempDir()
		if err := os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := WorktreeGitDirs(work)
		gotSet := map[string]bool{}
		for _, p := range got {
			gotSet[p] = true
		}
		if !gotSet[gitDir] {
			t.Errorf("missing worktree gitdir %q in %v", gitDir, got)
		}
		if !gotSet[commonDir] {
			t.Errorf("missing common dir %q in %v", commonDir, got)
		}
	})

	t.Run("linked worktree without commondir file falls back structurally", func(t *testing.T) {
		parent := t.TempDir()
		commonDir := filepath.Join(parent, ".git")
		gitDir := filepath.Join(commonDir, "worktrees", "wt1")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		work := t.TempDir()
		if err := os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: "+gitDir), 0o644); err != nil {
			t.Fatal(err)
		}
		got := WorktreeGitDirs(work)
		gotSet := map[string]bool{}
		for _, p := range got {
			gotSet[p] = true
		}
		if !gotSet[commonDir] {
			t.Errorf("structural fallback missing common dir %q in %v", commonDir, got)
		}
	})
}
