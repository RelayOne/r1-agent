package skill

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests for STOKE_SKILLS_DIR env override on DefaultRegistry.
// Spec: CLOUDSWARM-R1-INTEGRATION.md §2.2 / §10.2 — CloudSwarm
// supervisor writes unified-registry skills into an external
// directory and expects the subprocess to read from there.

func TestDefaultRegistry_HonorsStokeSkillsDirEnv(t *testing.T) {
	// Create an external skills dir, drop a flat-file skill in it,
	// point STOKE_SKILLS_DIR at that dir, and confirm DefaultRegistry
	// picks the skill up (proving dirs[0] was replaced).
	extDir := t.TempDir()

	skillMD := `---
name: env-override-probe
description: Probe skill used to verify STOKE_SKILLS_DIR is honored
---
# env-override-probe

> Probe skill used to verify STOKE_SKILLS_DIR is honored

This skill exists only to be discovered from the STOKE_SKILLS_DIR path.
`
	if err := os.WriteFile(filepath.Join(extDir, "env-override-probe.md"), []byte(skillMD), 0o600); err != nil {
		t.Fatalf("write probe skill: %v", err)
	}

	// Sandbox HOME so user skill dirs don't pollute discovery.
	// projectRoot is a fresh tempdir whose .stoke/skills/ does NOT exist,
	// which — absent the env override — would leave the probe undiscoverable.
	proj := t.TempDir()
	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("STOKE_SKILLS_DIR", extDir)

	r := DefaultRegistry(proj)
	if err := r.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := r.Get("env-override-probe")
	if got == nil {
		t.Fatalf("probe skill not discovered; registry has %d skills", len(r.skills))
	}
	if got.Path != filepath.Join(extDir, "env-override-probe.md") {
		t.Errorf("probe path=%q want %q", got.Path, filepath.Join(extDir, "env-override-probe.md"))
	}
}

func TestDefaultRegistry_FallsBackToProjectDotStokeSkillsWhenEnvUnset(t *testing.T) {
	proj := t.TempDir()
	home := t.TempDir()

	// Write the probe skill into the project's default path
	// (.stoke/skills/) and verify DefaultRegistry still picks it up
	// when STOKE_SKILLS_DIR is unset.
	projectSkillsDir := filepath.Join(proj, ".stoke", "skills")
	if err := os.MkdirAll(projectSkillsDir, 0o755); err != nil {
		t.Fatalf("mkdir project skills: %v", err)
	}
	skillMD := `---
name: default-path-probe
description: Probe skill used to verify project default skills path
---
# default-path-probe

> Probe skill
`
	if err := os.WriteFile(filepath.Join(projectSkillsDir, "default-path-probe.md"), []byte(skillMD), 0o600); err != nil {
		t.Fatalf("write probe: %v", err)
	}

	t.Setenv("HOME", home)
	// Ensure env is NOT set.
	t.Setenv("STOKE_SKILLS_DIR", "")

	r := DefaultRegistry(proj)
	if err := r.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := r.Get("default-path-probe")
	if got == nil {
		t.Fatalf("default-path probe not discovered; registry has %d skills", len(r.skills))
	}
}

func TestDefaultRegistry_EmptyStokeSkillsDirFallsBack(t *testing.T) {
	// An explicitly-empty STOKE_SKILLS_DIR must be treated as unset
	// (whitespace-only trimmed). Drops the probe at the default path
	// and expects discovery to succeed despite the env being present.
	proj := t.TempDir()
	home := t.TempDir()

	projectSkillsDir := filepath.Join(proj, ".stoke", "skills")
	if err := os.MkdirAll(projectSkillsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	skillMD := "---\nname: ws-probe\ndescription: Probe\n---\n# ws-probe\n\n> Probe\n"
	if err := os.WriteFile(filepath.Join(projectSkillsDir, "ws-probe.md"), []byte(skillMD), 0o600); err != nil {
		t.Fatalf("write probe: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("STOKE_SKILLS_DIR", "   ")

	r := DefaultRegistry(proj)
	if err := r.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.Get("ws-probe") == nil {
		t.Fatalf("whitespace-only env should fall back to project default; not found")
	}
}
