package ideinstall

// jetbrains.go — JetBrains installer (spec §T6). Bypasses the
// JSON-merge primitive because JetBrains has no documented config
// surface for registering external MCP servers; instead R1 ships a
// bundled `.jar` (built from ide/jetbrains/) that the operator
// drops into the IDE's plugins directory.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// jarSearchPaths lists candidate filesystem locations for the bundled
// `r1-mcp-bridge.jar`. The first match wins. Order:
//
//  1. R1_JETBRAINS_JAR env var (operator escape hatch).
//  2. <r1-binary>/../../share/r1/plugins/r1-mcp-bridge.jar
//     (install-prefix layout written by `make install`).
//  3. <CWD>/ide/jetbrains/build/distributions/r1-mcp-bridge.jar
//     (developer-checkout layout written by `./gradlew buildPlugin`).
//
// All three are documented in the README quickstart so operators
// know which one R1 found if a stale jar surprises them.
func jarSearchPaths() []string {
	var out []string
	if env := strings.TrimSpace(os.Getenv("R1_JETBRAINS_JAR")); env != "" {
		out = append(out, env)
	}
	if exe, err := os.Executable(); err == nil {
		// share/r1/plugins next to the bin/ that holds `r1`.
		out = append(out, filepath.Join(filepath.Dir(exe), "..", "share", "r1", "plugins", JarName))
	}
	// LINT-ALLOW chdir-cli-entry: r1 ide install jetbrains; read-only Getwd anchors the install dir.
	if wd, err := os.Getwd(); err == nil {
		// Developer checkout: built from ide/jetbrains/.
		out = append(out, filepath.Join(wd, "ide", "jetbrains", "build", "distributions", JarName))
	}
	return out
}

// JetBrainsJarFetcher abstracts the network fallback so tests can
// inject a stub instead of hitting GitHub.
type JetBrainsJarFetcher func(version string) (data []byte, sourceURL string, err error)

// DefaultJetBrainsJarFetcher downloads the release asset from
// https://github.com/RelayOne/r1/releases/download/v<ver>/r1-mcp-bridge-<ver>.jar.
// Returns the response bytes and the URL it pulled from.
func DefaultJetBrainsJarFetcher(version string) ([]byte, string, error) {
	if strings.TrimSpace(version) == "" {
		version = "latest"
	}
	url := fmt.Sprintf(
		"https://github.com/RelayOne/r1/releases/download/v%s/r1-mcp-bridge-%s.jar",
		version, version)
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, url, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, url, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, url, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, url, err
	}
	return data, url, nil
}

// InstallJetBrainsOpts parameterises InstallJetBrains so tests can
// inject a stub fetcher and so the CLI layer can pass the operator's
// --ide / --version flags through verbatim.
type InstallJetBrainsOpts struct {
	IDE     string             // IntelliJIdea | GoLand | PyCharm | WebStorm
	Version string             // empty → auto-detect highest installed
	Fetcher JetBrainsJarFetcher // nil → use DefaultJetBrainsJarFetcher
	// SkipDownload forces InstallJetBrains to skip the network
	// fallback when no local jar is found. Set to true in tests.
	SkipDownload bool
	// JarOverride lets tests bypass jar discovery entirely by handing
	// the installer the bytes directly. Used by jetbrains_test.go to
	// exercise the atomic-copy path without a real jar on disk.
	JarOverride []byte
}

// InstallJetBrains copies the bundled `r1-mcp-bridge.jar` to the
// per-IDE plugins directory. Returns the destination path + the
// install Action. Exit-code mapping happens at the CLI layer:
// missing jar with no network → exit 4 per spec §Public surface.
func InstallJetBrains(opts InstallJetBrainsOpts) (Result, error) {
	ide := opts.IDE
	if ide == "" {
		ide = "IntelliJIdea"
	}
	if !validJetBrainsIDE(ide) {
		return Result{}, fmt.Errorf("unknown jetbrains ide %q (valid: %s)",
			ide, strings.Join(ValidJetBrainsIDEs(), ", "))
	}
	pluginsDir, version, err := JetBrainsPluginsDir(ide, opts.Version)
	if err != nil {
		return Result{}, err
	}
	dst := filepath.Join(pluginsDir, JarName)

	// Path A: caller provided bytes directly (test injection).
	if len(opts.JarOverride) > 0 {
		if err := writeAtomic(dst, opts.JarOverride); err != nil {
			return Result{Path: dst}, fmt.Errorf("write jar: %w", err)
		}
		return Result{Path: dst, Action: ActionAdded}, nil
	}

	// Path B: search the local filesystem.
	for _, candidate := range jarSearchPaths() {
		if _, err := os.Stat(candidate); err == nil {
			if err := copyFile(candidate, dst); err != nil {
				return Result{Path: dst}, fmt.Errorf("copy jar: %w", err)
			}
			return Result{Path: dst, Action: ActionAdded, /* source kept in Path */}, nil
		}
	}

	// Path C: network fallback (operator may have opted out).
	if opts.SkipDownload {
		return Result{Path: dst}, errors.New("jetbrains jar not found on disk and download disabled")
	}
	fetcher := opts.Fetcher
	if fetcher == nil {
		fetcher = DefaultJetBrainsJarFetcher
	}
	data, src, err := fetcher(strings.TrimPrefix(version, "v"))
	if err != nil {
		return Result{Path: dst}, fmt.Errorf("download jetbrains jar from %s: %w", src, err)
	}
	if err := writeAtomic(dst, data); err != nil {
		return Result{Path: dst}, fmt.Errorf("write jar from %s: %w", src, err)
	}
	return Result{Path: dst, Action: ActionAdded}, nil
}

// UninstallJetBrains deletes the bundled jar from the resolved
// plugins dir. Other jars in the plugins dir are untouched.
func UninstallJetBrains(ide, version string) (Result, error) {
	if ide == "" {
		ide = "IntelliJIdea"
	}
	if !validJetBrainsIDE(ide) {
		return Result{}, fmt.Errorf("unknown jetbrains ide %q (valid: %s)",
			ide, strings.Join(ValidJetBrainsIDEs(), ", "))
	}
	pluginsDir, _, err := JetBrainsPluginsDir(ide, version)
	if err != nil {
		return Result{}, err
	}
	dst := filepath.Join(pluginsDir, JarName)
	if _, err := os.Stat(dst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{Path: dst}, fmt.Errorf(
				"r1 is not installed in jetbrains (%s)", pluginsDir)
		}
		return Result{Path: dst}, err
	}
	if err := os.Remove(dst); err != nil {
		return Result{Path: dst}, fmt.Errorf("remove jar: %w", err)
	}
	return Result{Path: dst, Action: ActionRemoved}, nil
}

// IsJetBrainsRegistered returns whether the bundled jar is present
// under the highest-versioned IntelliJ IDEA plugins dir. Used by
// Verify; jetbrains-detect failures are reported as `not-found`.
func IsJetBrainsRegistered(ide, version string) (bool, string, error) {
	if ide == "" {
		ide = "IntelliJIdea"
	}
	pluginsDir, _, err := JetBrainsPluginsDir(ide, version)
	if err != nil {
		return false, "", err
	}
	dst := filepath.Join(pluginsDir, JarName)
	_, err = os.Stat(dst)
	if err == nil {
		return true, pluginsDir, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, pluginsDir, nil
	}
	return false, pluginsDir, err
}

// PrintJetBrainsConfirmation prints the post-install confirmation
// lines per spec §T6.4.
func PrintJetBrainsConfirmation(w io.Writer, res Result, ide string) {
	switch res.Action {
	case ActionNoop:
		fmt.Fprintf(w, "no change: %s already present in %s\n", JarName, res.Path)
	default:
		fmt.Fprintf(w, "installed: %s -> %s\n", JarName, res.Path)
	}
	fmt.Fprintf(w, "restart %s to load the plugin.\n", ide)
}
