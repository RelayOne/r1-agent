package pools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTestHome overrides HOME so that StorePath()/ManifestPath() use a temp dir.
func withTestHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func TestManifest_SaveLoad(t *testing.T) {
	withTestHome(t)

	m := &Manifest{
		Pools: []Pool{
			{
				ID:        "claude-1",
				Label:     "Test Pool",
				ConfigDir: "/tmp/pools/claude-1",
				Provider:  "claude",
				AccountID: "user@example.com",
				AddedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		},
	}

	if err := m.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Verify file was written
	data, err := os.ReadFile(ManifestPath())
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	var decoded Manifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if len(decoded.Pools) != 1 {
		t.Fatalf("loaded %d pools, want 1", len(decoded.Pools))
	}
	if decoded.Pools[0].ID != "claude-1" {
		t.Errorf("Pool ID = %q, want %q", decoded.Pools[0].ID, "claude-1")
	}
	if decoded.Pools[0].Label != "Test Pool" {
		t.Errorf("Pool Label = %q, want %q", decoded.Pools[0].Label, "Test Pool")
	}

	// Reload via LoadManifest
	loaded, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest() error: %v", err)
	}
	if len(loaded.Pools) != 1 {
		t.Fatalf("LoadManifest() returned %d pools, want 1", len(loaded.Pools))
	}
	if loaded.Pools[0].AccountID != "user@example.com" {
		t.Errorf("AccountID = %q, want %q", loaded.Pools[0].AccountID, "user@example.com")
	}
}

func TestManifest_LoadEmpty(t *testing.T) {
	withTestHome(t)

	// No manifest file exists yet -- should return empty manifest, not error
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest() error: %v", err)
	}
	if len(m.Pools) != 0 {
		t.Errorf("Pools = %d, want 0", len(m.Pools))
	}
}

func TestManifest_FindByAccount(t *testing.T) {
	m := &Manifest{
		Pools: []Pool{
			{ID: "claude-1", AccountID: "alice@example.com", Provider: "claude"},
			{ID: "codex-1", AccountID: "bob@example.com", Provider: "codex"},
		},
	}

	// Found
	p := m.FindByAccount("alice@example.com")
	if p == nil {
		t.Fatal("FindByAccount(alice) returned nil, want pool")
	}
	if p.ID != "claude-1" {
		t.Errorf("found pool ID = %q, want %q", p.ID, "claude-1")
	}

	// Not found
	p = m.FindByAccount("nobody@example.com")
	if p != nil {
		t.Errorf("FindByAccount(nobody) = %+v, want nil", p)
	}
}

func TestManifest_ClaudeDirs_CodexDirs(t *testing.T) {
	m := &Manifest{
		Pools: []Pool{
			{ID: "claude-1", ConfigDir: "/pools/claude-1", Provider: "claude"},
			{ID: "claude-2", ConfigDir: "/pools/claude-2", Provider: "claude"},
			{ID: "codex-1", ConfigDir: "/pools/codex-1", Provider: "codex"},
		},
	}

	claudeDirs := m.ClaudeDirs()
	if len(claudeDirs) != 2 {
		t.Errorf("ClaudeDirs() = %d dirs, want 2", len(claudeDirs))
	}

	codexDirs := m.CodexDirs()
	if len(codexDirs) != 1 {
		t.Errorf("CodexDirs() = %d dirs, want 1", len(codexDirs))
	}
	if codexDirs[0] != "/pools/codex-1" {
		t.Errorf("CodexDirs()[0] = %q, want %q", codexDirs[0], "/pools/codex-1")
	}
}

func TestManifest_NextID(t *testing.T) {
	m := &Manifest{}

	// Empty manifest
	if got := m.NextID("claude"); got != "claude-1" {
		t.Errorf("NextID(claude) = %q, want %q", got, "claude-1")
	}

	m.Pools = append(m.Pools,
		Pool{ID: "claude-1", Provider: "claude"},
		Pool{ID: "claude-2", Provider: "claude"},
		Pool{ID: "codex-1", Provider: "codex"},
	)

	if got := m.NextID("claude"); got != "claude-3" {
		t.Errorf("NextID(claude) = %q, want %q", got, "claude-3")
	}
	if got := m.NextID("codex"); got != "codex-2" {
		t.Errorf("NextID(codex) = %q, want %q", got, "codex-2")
	}
}

func TestPool_IsContainer(t *testing.T) {
	host := Pool{ID: "claude-1", Runtime: ""}
	if host.IsContainer() {
		t.Error("empty runtime should not be container")
	}
	hostExplicit := Pool{ID: "claude-2", Runtime: RuntimeHost}
	if hostExplicit.IsContainer() {
		t.Error("host runtime should not be container")
	}
	container := Pool{ID: "claude-3", Runtime: RuntimeContainer, ContainerVol: "stoke-pool-claude-3"}
	if !container.IsContainer() {
		t.Error("container runtime should be container")
	}
}

func TestManifest_ListContainerPools(t *testing.T) {
	m := &Manifest{
		Pools: []Pool{
			{ID: "claude-1", Provider: "claude", Runtime: RuntimeHost},
			{ID: "claude-2", Provider: "claude", Runtime: RuntimeContainer, ContainerVol: "vol-2"},
			{ID: "codex-1", Provider: "codex", Runtime: ""},
			{ID: "claude-3", Provider: "claude", Runtime: RuntimeContainer, ContainerVol: "vol-3"},
		},
	}

	containers := m.ListContainerPools()
	if len(containers) != 2 {
		t.Fatalf("ListContainerPools() = %d, want 2", len(containers))
	}
	if containers[0].ID != "claude-2" || containers[1].ID != "claude-3" {
		t.Errorf("unexpected container pools: %+v", containers)
	}
}

func TestDockerExecArgs(t *testing.T) {
	// Host pool returns nil
	host := Pool{ID: "claude-1", Runtime: RuntimeHost}
	if args := DockerExecArgs(host, "image:latest", "/work"); args != nil {
		t.Errorf("host pool should return nil, got %v", args)
	}

	// Container pool returns docker run args
	container := Pool{
		ID:           "claude-2",
		Runtime:      RuntimeContainer,
		ContainerVol: "stoke-pool-claude-2",
		ConfigDir:    "/config",
	}
	args := DockerExecArgs(container, "ghcr.io/ericmacdougall/stoke-pool:latest", "/workspace/task-1")
	if len(args) == 0 {
		t.Fatal("container pool should return docker args")
	}
	if args[0] != "docker" || args[1] != "run" {
		t.Errorf("expected docker run, got %v", args[:2])
	}
	// Check volume mount is present
	found := false
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && args[i+1] == "stoke-pool-claude-2:/config" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("volume mount not found in args: %v", args)
	}
}

func TestRemovePool(t *testing.T) {
	home := withTestHome(t)

	// Create a pool with a config dir
	configDir := filepath.Join(home, ".stoke", "pools", "claude-1")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Write a dummy file to verify cleanup
	if err := os.WriteFile(filepath.Join(configDir, "creds.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Pools: []Pool{
			{ID: "claude-1", ConfigDir: configDir, Provider: "claude", AccountID: "test"},
			{ID: "codex-1", ConfigDir: "/tmp/codex-1", Provider: "codex", AccountID: "test2"},
		},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	// Remove existing pool
	if err := RemovePool("claude-1"); err != nil {
		t.Fatalf("RemovePool(claude-1) error: %v", err)
	}

	// Verify pool was removed from manifest
	loaded, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Pools) != 1 {
		t.Errorf("after remove: %d pools, want 1", len(loaded.Pools))
	}
	if loaded.Pools[0].ID != "codex-1" {
		t.Errorf("remaining pool = %q, want %q", loaded.Pools[0].ID, "codex-1")
	}

	// Verify config dir was cleaned up
	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Errorf("config dir %s still exists after RemovePool", configDir)
	}

	// Remove missing pool should error
	if err := RemovePool("nonexistent-99"); err == nil {
		t.Error("RemovePool(nonexistent) expected error, got nil")
	}
}
