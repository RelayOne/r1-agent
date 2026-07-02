package wizard

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed githook/pre-commit-ledger-guard.sh
var ledgerGuardScript []byte

// InstallLedgerGuardHook installs (or appends) the ledger append-only guard
// into the repository's .git/hooks/pre-commit hook. It is idempotent: if the
// guard marker is already present the function is a no-op.
//
// Called from `r1 init` (cmd/r1/main.go initCmd) so every initialized project
// gets the append-only ledger protection immediately.
func InstallLedgerGuardHook(repoRoot string) error {
	hooksDir := filepath.Join(repoRoot, ".git", "hooks")
	info, err := os.Stat(hooksDir)
	if err != nil {
		return fmt.Errorf("wizard: .git/hooks not found: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("wizard: .git/hooks is not a directory")
	}

	hookPath := filepath.Join(hooksDir, "pre-commit")
	existing, err := os.ReadFile(hookPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("wizard: read pre-commit hook: %w", err)
	}

	// Already installed — nothing to do.
	if strings.Contains(string(existing), "STOKE LEDGER GUARD") {
		return nil
	}

	if os.IsNotExist(err) || len(existing) == 0 {
		// No existing hook — write ours directly.
		if err := os.WriteFile(hookPath, ledgerGuardScript, 0755); err != nil { // #nosec G306 -- hook script requires executable permission; written to user-owned repo.
			return fmt.Errorf("wizard: write pre-commit hook: %w", err)
		}
		return nil
	}

	// Existing hook without our guard — append.
	combined := string(existing) + "\n" + string(ledgerGuardScript)
	if err := os.WriteFile(hookPath, []byte(combined), 0755); err != nil { // #nosec G306 -- hook script requires executable permission; written to user-owned repo.
		return fmt.Errorf("wizard: append to pre-commit hook: %w", err)
	}
	return nil
}
