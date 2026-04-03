package pools

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// StorePath returns ~/.stoke/pools
func StorePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".stoke", "pools")
}

// ManifestPath returns ~/.stoke/pools/manifest.json
func ManifestPath() string {
	return filepath.Join(StorePath(), "manifest.json")
}

// Pool is one registered subscription pool.
type Pool struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`       // user-friendly name
	ConfigDir string    `json:"config_dir"`  // path to credential dir
	Provider  string    `json:"provider"`    // "claude" or "codex"
	AccountID string    `json:"account_id"`  // dedup key (from credentials)
	AddedAt   time.Time `json:"added_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

// Manifest is the persisted pool registry.
type Manifest struct {
	Pools []Pool `json:"pools"`
}

// LoadManifest reads the pool manifest.
func LoadManifest() (*Manifest, error) {
	data, err := os.ReadFile(ManifestPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Manifest{}, nil
		}
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Save writes the manifest to disk.
func (m *Manifest) Save() error {
	if err := os.MkdirAll(filepath.Dir(ManifestPath()), 0755); err != nil {
		return fmt.Errorf("create pools dir: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ManifestPath(), data, 0644)
}

// FindByAccount returns the pool with this account ID, or nil.
func (m *Manifest) FindByAccount(accountID string) *Pool {
	for i := range m.Pools {
		if m.Pools[i].AccountID == accountID {
			return &m.Pools[i]
		}
	}
	return nil
}

// ClaudeDirs returns all Claude pool config dirs.
func (m *Manifest) ClaudeDirs() []string {
	var dirs []string
	for _, p := range m.Pools {
		if p.Provider == "claude" {
			dirs = append(dirs, p.ConfigDir)
		}
	}
	return dirs
}

// CodexDirs returns all Codex pool config dirs.
func (m *Manifest) CodexDirs() []string {
	var dirs []string
	for _, p := range m.Pools {
		if p.Provider == "codex" {
			dirs = append(dirs, p.ConfigDir)
		}
	}
	return dirs
}

// NextID returns the next pool ID like "claude-3"
func (m *Manifest) NextID(provider string) string {
	max := 0
	prefix := provider + "-"
	for _, p := range m.Pools {
		if strings.HasPrefix(p.ID, prefix) {
			var n int
			fmt.Sscanf(p.ID, prefix+"%d", &n)
			if n > max {
				max = n
			}
		}
	}
	return fmt.Sprintf("%s-%d", provider, max+1)
}

// AddClaude runs the Claude Code OAuth login flow and registers the pool.
// Returns the pool ID on success.
func AddClaude(claudeBin, label string) (string, error) {
	manifest, err := LoadManifest()
	if err != nil {
		return "", fmt.Errorf("load manifest: %w", err)
	}

	poolID := manifest.NextID("claude")
	configDir := filepath.Join(StorePath(), poolID)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return "", fmt.Errorf("create pool dir: %w", err)
	}

	fmt.Printf("  Pool: %s\n", poolID)
	fmt.Printf("  Credentials dir: %s\n", configDir)
	fmt.Println()

	// Run claude login with CLAUDE_CONFIG_DIR pointing to our pool dir
	// This opens the browser, user authenticates, pastes the code
	cmd := exec.Command(claudeBin, "login")
	cmd.Dir = configDir
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("  Launching Claude Code login...")
	fmt.Println("  Sign in with your Max plan account.")
	fmt.Println()

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude login failed: %w", err)
	}

	// Verify credentials were created
	accountID := readAccountID(configDir)
	if accountID == "" {
		// Try reading the token as fallback
		token := readToken(configDir)
		if token == "" {
			os.RemoveAll(configDir)
			return "", fmt.Errorf("login completed but no credentials found in %s", configDir)
		}
		// Use a hash of the token as account ID
		accountID = "token-" + token[:min(16, len(token))]
	}

	// Dedup check: if this account is already registered, refresh it
	if existing := manifest.FindByAccount(accountID); existing != nil {
		fmt.Printf("\n  Account already registered as %s (%s)\n", existing.ID, existing.Label)
		fmt.Println("  Refreshing credentials...")

		// Copy new credentials over old
		copyCredentials(configDir, existing.ConfigDir)
		os.RemoveAll(configDir) // remove temp dir
		existing.LastUsed = time.Now()
		if err := manifest.Save(); err != nil {
			return "", fmt.Errorf("save manifest: %w", err)
		}

		fmt.Printf("  Refreshed: %s\n", existing.ID)
		return existing.ID, nil
	}

	// Register new pool
	if label == "" {
		label = fmt.Sprintf("Claude Max #%s", strings.TrimPrefix(poolID, "claude-"))
	}

	pool := Pool{
		ID:        poolID,
		Label:     label,
		ConfigDir: configDir,
		Provider:  "claude",
		AccountID: accountID,
		AddedAt:   time.Now(),
	}

	manifest.Pools = append(manifest.Pools, pool)
	if err := manifest.Save(); err != nil {
		return "", fmt.Errorf("save manifest: %w", err)
	}

	fmt.Printf("\n  Added: %s (%s)\n", poolID, label)
	fmt.Printf("  Total Claude pools: %d\n", len(manifest.ClaudeDirs()))
	return poolID, nil
}

// RemovePool removes a pool by ID.
func RemovePool(poolID string) error {
	manifest, err := LoadManifest()
	if err != nil {
		return err
	}

	var kept []Pool
	var removed *Pool
	for i := range manifest.Pools {
		if manifest.Pools[i].ID == poolID {
			removed = &manifest.Pools[i]
		} else {
			kept = append(kept, manifest.Pools[i])
		}
	}

	if removed == nil {
		return fmt.Errorf("pool %s not found", poolID)
	}

	manifest.Pools = kept
	if err := manifest.Save(); err != nil {
		return err
	}

	// Remove credentials dir
	os.RemoveAll(removed.ConfigDir)
	return nil
}

// claudeCredentials is the typed structure of Claude Code's .credentials.json.
type claudeCredentials struct {
	ClaudeAiOauth struct {
		Email       string `json:"email"`
		AccountID   string `json:"accountId"`
		AccessToken string `json:"accessToken"`
	} `json:"claudeAiOauth"`
}

// readAccountID extracts an account identifier from credentials.
func readAccountID(configDir string) string {
	data, err := os.ReadFile(filepath.Join(configDir, ".credentials.json"))
	if err != nil {
		return ""
	}

	var creds claudeCredentials
	if json.Unmarshal(data, &creds) != nil {
		return ""
	}

	if creds.ClaudeAiOauth.Email != "" {
		return creds.ClaudeAiOauth.Email
	}
	if creds.ClaudeAiOauth.AccountID != "" {
		return creds.ClaudeAiOauth.AccountID
	}
	if token := creds.ClaudeAiOauth.AccessToken; token != "" {
		return "tok-" + token[:min(16, len(token))]
	}

	return ""
}

func readToken(configDir string) string {
	data, err := os.ReadFile(filepath.Join(configDir, ".credentials.json"))
	if err != nil {
		return ""
	}
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(data, &creds) != nil {
		return ""
	}
	return creds.ClaudeAiOauth.AccessToken
}

// AddCodex runs the Codex CLI login flow and registers the pool.
func AddCodex(codexBin, label string) (string, error) {
	manifest, err := LoadManifest()
	if err != nil {
		return "", fmt.Errorf("load manifest: %w", err)
	}

	poolID := manifest.NextID("codex")
	configDir := filepath.Join(StorePath(), poolID)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return "", fmt.Errorf("create pool dir: %w", err)
	}

	fmt.Printf("  Pool: %s\n", poolID)
	fmt.Printf("  Credentials dir: %s\n", configDir)
	fmt.Println()

	// Run codex login with CODEX_HOME pointing to our pool dir
	cmd := exec.Command(codexBin, "auth", "login")
	cmd.Dir = configDir
	cmd.Env = append(os.Environ(), "CODEX_HOME="+configDir)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("  Launching Codex login...")
	fmt.Println("  Sign in with your OpenAI account.")
	fmt.Println()

	if err := cmd.Run(); err != nil {
		// Codex may not have an explicit login command -- try alternative
		cmd2 := exec.Command(codexBin, "login")
		cmd2.Dir = configDir
		cmd2.Env = append(os.Environ(), "CODEX_HOME="+configDir)
		cmd2.Stdin = os.Stdin
		cmd2.Stdout = os.Stdout
		cmd2.Stderr = os.Stderr
		if err2 := cmd2.Run(); err2 != nil {
			os.RemoveAll(configDir)
			return "", fmt.Errorf("codex login failed: %w (also tried 'auth login': %w)", err2, err)
		}
	}

	// Read account identifier from Codex credentials
	accountID := readCodexAccountID(configDir)
	if accountID == "" {
		accountID = "codex-" + poolID // fallback
	}

	// Dedup
	if existing := manifest.FindByAccount(accountID); existing != nil && existing.Provider == "codex" {
		fmt.Printf("\n  Account already registered as %s (%s)\n", existing.ID, existing.Label)
		fmt.Println("  Refreshing credentials...")
		copyCredentials(configDir, existing.ConfigDir)
		os.RemoveAll(configDir)
		existing.LastUsed = time.Now()
		if err := manifest.Save(); err != nil {
			return "", fmt.Errorf("save manifest: %w", err)
		}
		fmt.Printf("  Refreshed: %s\n", existing.ID)
		return existing.ID, nil
	}

	if label == "" {
		label = fmt.Sprintf("Codex #%s", strings.TrimPrefix(poolID, "codex-"))
	}

	pool := Pool{
		ID:        poolID,
		Label:     label,
		ConfigDir: configDir,
		Provider:  "codex",
		AccountID: accountID,
		AddedAt:   time.Now(),
	}

	manifest.Pools = append(manifest.Pools, pool)
	if err := manifest.Save(); err != nil {
		return "", fmt.Errorf("save manifest: %w", err)
	}

	fmt.Printf("\n  Added: %s (%s)\n", poolID, label)
	fmt.Printf("  Total Codex pools: %d\n", len(manifest.CodexDirs()))
	return poolID, nil
}

// codexCredentials is the typed union of known Codex credential formats.
type codexCredentials struct {
	Email       string `json:"email"`
	AccountID   string `json:"accountId"`
	AccountID2  string `json:"account_id"`
	UserID      string `json:"user_id"`
	AccessToken string `json:"accessToken"`
	AccessTok2  string `json:"access_token"`
	APIKey      string `json:"api_key"`
	Token       string `json:"token"`
}

// firstNonEmpty returns the first non-empty string from the list.
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func readCodexAccountID(configDir string) string {
	for _, name := range []string{".codex-credentials.json", "credentials.json", "auth.json"} {
		data, err := os.ReadFile(filepath.Join(configDir, name))
		if err != nil {
			continue
		}

		var creds codexCredentials
		if json.Unmarshal(data, &creds) != nil {
			continue
		}

		if id := firstNonEmptyStr(creds.Email, creds.AccountID, creds.AccountID2, creds.UserID); id != "" {
			return id
		}
		if token := firstNonEmptyStr(creds.AccessToken, creds.AccessTok2, creds.APIKey, creds.Token); len(token) > 16 {
			return "codex-tok-" + token[:16]
		}
	}
	return ""
}

func copyCredentials(src, dst string) {
	if err := os.MkdirAll(dst, 0700); err != nil {
		return
	}
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0600); err != nil {
			continue
		}
	}
}

