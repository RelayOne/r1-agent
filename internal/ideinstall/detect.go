package ideinstall

// detect.go — Detector interface + per-IDE detectors + the
// `DetectorFor` factory used by every CLI dispatch path in
// `cmd/r1/ide_install_cmd.go`. See specs/mcp-ide-bundles.md §T2.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// IDE name constants — single source of truth. Keep alphabetised so
// help text and Verify output stay in stable order.
const (
	IDECursor    = "cursor"
	IDEJetBrains = "jetbrains"
	IDEVSCode    = "vscode"
	IDEWindsurf  = "windsurf"
)

// SupportedIDEs is the stable iteration order used by Verify and the
// CLI help text. Cursor → Windsurf → VS Code → JetBrains mirrors the
// order in the spec's acceptance criteria.
var SupportedIDEs = []string{IDECursor, IDEWindsurf, IDEVSCode, IDEJetBrains}

// Detector resolves the config path for one IDE. Per D-4 of the spec
// detection probes the canonical config dir, not $PATH — Flatpak /
// Snap / Toolbox installs put the binary off PATH but the config dir
// in the standard location.
type Detector interface {
	IDE() string
	// DetectConfig returns the resolved path for the requested scope
	// plus a `found` flag indicating whether the parent dir exists
	// today. found == false is non-fatal — the caller decides whether
	// to MkdirAll or report not-found.
	DetectConfig(scope Scope, workspaceDir string) (path string, found bool, err error)
	SupportsScope(scope Scope) bool
}

// DetectorFor returns the detector for `ide`, or an error enumerating
// every valid name. Used by the CLI dispatcher and by Verify.
func DetectorFor(ide string) (Detector, error) {
	switch strings.ToLower(strings.TrimSpace(ide)) {
	case IDECursor:
		return cursorDetector{}, nil
	case IDEWindsurf:
		return windsurfDetector{}, nil
	case IDEVSCode:
		return vscodeDetector{}, nil
	case IDEJetBrains:
		return jetbrainsDetector{}, nil
	default:
		return nil, fmt.Errorf("unknown ide %q (valid: %s)",
			ide, strings.Join(SupportedIDEs, ", "))
	}
}

// DefaultScopeFor returns the per-IDE default scope per T1.2 of the
// spec: Cursor + VS Code → workspace, Windsurf + JetBrains → global.
func DefaultScopeFor(ide string) Scope {
	switch strings.ToLower(strings.TrimSpace(ide)) {
	case IDECursor, IDEVSCode:
		return ScopeWorkspace
	default:
		return ScopeGlobal
	}
}

// homeDir resolves the user's home dir via os.UserHomeDir — single
// indirection point so build-tagged tests can override via t.Setenv.
func homeDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	if strings.TrimSpace(h) == "" {
		return "", errors.New("resolve user home: empty")
	}
	return h, nil
}

// --- Cursor -------------------------------------------------------

type cursorDetector struct{}

func (cursorDetector) IDE() string { return IDECursor }

func (cursorDetector) SupportsScope(s Scope) bool {
	return s == ScopeWorkspace || s == ScopeGlobal
}

func (cursorDetector) DetectConfig(scope Scope, workspaceDir string) (string, bool, error) {
	if scope == ScopeWorkspace {
		if strings.TrimSpace(workspaceDir) == "" {
			wd, err := os.Getwd()
			if err != nil {
				return "", false, fmt.Errorf("resolve workspace dir: %w", err)
			}
			workspaceDir = wd
		}
		p := filepath.Join(workspaceDir, ".cursor", "mcp.json")
		_, err := os.Stat(filepath.Dir(p))
		return p, err == nil, nil
	}
	h, err := homeDir()
	if err != nil {
		return "", false, err
	}
	p := filepath.Join(h, ".cursor", "mcp.json")
	_, err = os.Stat(filepath.Dir(p))
	return p, err == nil, nil
}

// --- Windsurf -----------------------------------------------------

type windsurfDetector struct{}

func (windsurfDetector) IDE() string { return IDEWindsurf }

// Windsurf is global-only per spec §Public surface. Workspace-scope
// requests are rejected at the CLI layer (T1.2) before reaching the
// detector, but SupportsScope still returns the truth so generic
// callers (Verify) see the same answer.
func (windsurfDetector) SupportsScope(s Scope) bool {
	return s == ScopeGlobal
}

func (windsurfDetector) DetectConfig(scope Scope, _ string) (string, bool, error) {
	if scope == ScopeWorkspace {
		return "", false, errors.New("windsurf does not support workspace scope")
	}
	h, err := homeDir()
	if err != nil {
		return "", false, err
	}
	// On Windows the same layout lives under USERPROFILE, which
	// os.UserHomeDir already returns.
	p := filepath.Join(h, ".codeium", "windsurf", "mcp_config.json")
	_, err = os.Stat(filepath.Dir(p))
	return p, err == nil, nil
}

// --- VS Code ------------------------------------------------------

type vscodeDetector struct{}

func (vscodeDetector) IDE() string { return IDEVSCode }

func (vscodeDetector) SupportsScope(s Scope) bool {
	return s == ScopeWorkspace || s == ScopeGlobal
}

func (vscodeDetector) DetectConfig(scope Scope, workspaceDir string) (string, bool, error) {
	if scope == ScopeWorkspace {
		if strings.TrimSpace(workspaceDir) == "" {
			wd, err := os.Getwd()
			if err != nil {
				return "", false, fmt.Errorf("resolve workspace dir: %w", err)
			}
			workspaceDir = wd
		}
		p := filepath.Join(workspaceDir, ".vscode", "mcp.json")
		_, err := os.Stat(filepath.Dir(p))
		return p, err == nil, nil
	}
	p, err := vscodeUserConfigPath()
	if err != nil {
		return "", false, err
	}
	_, statErr := os.Stat(filepath.Dir(p))
	return p, statErr == nil, nil
}

// vscodeUserConfigPath resolves the per-platform user config path
// per T2.2: Linux → ~/.config/Code/User/mcp.json, macOS → ~/Library/
// Application Support/Code/User/mcp.json, Windows → %APPDATA%/Code/
// User/mcp.json (with %APPDATA% precedence over %USERPROFILE%).
func vscodeUserConfigPath() (string, error) {
	switch runtime.GOOS {
	case "windows":
		// APPDATA takes precedence; fall back to USERPROFILE+Roaming.
		if appdata := strings.TrimSpace(os.Getenv("APPDATA")); appdata != "" {
			return filepath.Join(appdata, "Code", "User", "mcp.json"), nil
		}
		h, err := homeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "AppData", "Roaming", "Code", "User", "mcp.json"), nil
	case "darwin":
		h, err := homeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "Library", "Application Support", "Code", "User", "mcp.json"), nil
	default:
		h, err := homeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, ".config", "Code", "User", "mcp.json"), nil
	}
}

// --- JetBrains ----------------------------------------------------

type jetbrainsDetector struct{}

func (jetbrainsDetector) IDE() string { return IDEJetBrains }

func (jetbrainsDetector) SupportsScope(s Scope) bool {
	return s == ScopeGlobal
}

// DetectConfig for JetBrains returns the plugins directory of the
// highest-versioned IntelliJ IDEA install on disk. Callers that want
// a different IDE (GoLand, PyCharm, WebStorm) or version go through
// JetBrainsPluginsDir directly.
func (d jetbrainsDetector) DetectConfig(scope Scope, _ string) (string, bool, error) {
	if scope == ScopeWorkspace {
		return "", false, errors.New("jetbrains does not support workspace scope")
	}
	dir, _, err := JetBrainsPluginsDir("IntelliJIdea", "")
	if err != nil {
		return "", false, err
	}
	_, statErr := os.Stat(dir)
	return dir, statErr == nil, nil
}

// jetbrainsConfigRoots returns the per-platform directories holding
// per-IDE config trees (e.g. `IntelliJIdea2026.2/`). Used by version
// auto-detection in JetBrainsPluginsDir.
func jetbrainsConfigRoots() ([]string, error) {
	h, err := homeDir()
	if err != nil {
		return nil, err
	}
	switch runtime.GOOS {
	case "windows":
		appdata := strings.TrimSpace(os.Getenv("APPDATA"))
		if appdata == "" {
			appdata = filepath.Join(h, "AppData", "Roaming")
		}
		return []string{filepath.Join(appdata, "JetBrains")}, nil
	case "darwin":
		return []string{
			filepath.Join(h, "Library", "Application Support", "JetBrains"),
		}, nil
	default:
		return []string{filepath.Join(h, ".config", "JetBrains")}, nil
	}
}

// jetbrainsPluginsRoots mirrors jetbrainsConfigRoots but points at
// the *plugins* tree (different layout on Linux vs the others).
func jetbrainsPluginsRoots() ([]string, error) {
	h, err := homeDir()
	if err != nil {
		return nil, err
	}
	switch runtime.GOOS {
	case "windows":
		appdata := strings.TrimSpace(os.Getenv("APPDATA"))
		if appdata == "" {
			appdata = filepath.Join(h, "AppData", "Roaming")
		}
		return []string{filepath.Join(appdata, "JetBrains")}, nil
	case "darwin":
		return []string{
			filepath.Join(h, "Library", "Application Support", "JetBrains"),
		}, nil
	default:
		return []string{filepath.Join(h, ".local", "share", "JetBrains")}, nil
	}
}

// JetBrainsPluginsDir resolves the plugins directory for a specific
// IDE + version. Empty version → auto-detect by scanning the per-OS
// config root for entries prefixed `<IDE>` and picking the highest
// semver-comparable tail. Returns the resolved directory, the
// detected version, and an error.
func JetBrainsPluginsDir(ide, version string) (string, string, error) {
	if ide == "" {
		ide = "IntelliJIdea"
	}
	if !validJetBrainsIDE(ide) {
		return "", "", fmt.Errorf("unknown jetbrains ide %q (valid: %s)",
			ide, strings.Join(ValidJetBrainsIDEs(), ", "))
	}
	if version == "" {
		v, err := detectHighestJetBrainsVersion(ide)
		if err != nil {
			return "", "", err
		}
		version = v
	}
	roots, err := jetbrainsPluginsRoots()
	if err != nil {
		return "", "", err
	}
	// Roots are length 1 today across every OS; loop for forward
	// compatibility (Toolbox installs may add another root).
	for _, root := range roots {
		return filepath.Join(root, ide+version, "plugins"), version, nil
	}
	return "", "", errors.New("no jetbrains plugins root resolved")
}

// detectHighestJetBrainsVersion lists the per-OS config root for
// directories whose names start with `<ide>` (e.g. IntelliJIdea2026.2)
// and returns the lexicographically highest version suffix. Returns a
// safe fallback `2026.1` when no install is detected so install can
// still proceed against a freshly-installed IDE that hasn't booted
// once (the post-install confirmation tells the user to restart).
func detectHighestJetBrainsVersion(ide string) (string, error) {
	roots, err := jetbrainsConfigRoots()
	if err != nil {
		return "", err
	}
	var versions []string
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("read jetbrains root %q: %w", root, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasPrefix(name, ide) {
				continue
			}
			tail := strings.TrimPrefix(name, ide)
			if tail == "" {
				continue
			}
			versions = append(versions, tail)
		}
	}
	if len(versions) == 0 {
		return "2026.1", nil
	}
	// Sort descending so versions[0] is the highest. Lexicographic
	// sort works for the `YYYY.N` pattern JetBrains uses today; we
	// pad the minor segment to two digits so `2026.10` outranks
	// `2026.2` instead of being mis-ordered as a string.
	sort.SliceStable(versions, func(i, j int) bool {
		return padJetBrainsVersion(versions[i]) > padJetBrainsVersion(versions[j])
	})
	return versions[0], nil
}

// padJetBrainsVersion turns `2026.2` into `2026.02` so naive string
// comparison handles double-digit minors correctly.
func padJetBrainsVersion(v string) string {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return v
	}
	if len(parts[1]) == 1 {
		return parts[0] + ".0" + parts[1]
	}
	return v
}

// ValidJetBrainsIDEs returns the four supported JetBrains IDE codes
// in canonical capitalisation. Surfaced from the CLI usage strings.
func ValidJetBrainsIDEs() []string {
	return []string{"IntelliJIdea", "GoLand", "PyCharm", "WebStorm"}
}

func validJetBrainsIDE(name string) bool {
	for _, v := range ValidJetBrainsIDEs() {
		if v == name {
			return true
		}
	}
	return false
}
