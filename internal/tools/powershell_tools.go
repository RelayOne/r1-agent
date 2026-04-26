// powershell_tools.go — powershell tool handler.
//
// T-R1P-015: PowerShell tool — executes a PowerShell script/command string.
//
// On Windows: uses `powershell.exe -NonInteractive -Command <cmd>` or
//
//	`pwsh -NonInteractive -Command <cmd>` (pwsh preferred when available).
//
// On non-Windows (Linux/macOS): uses `pwsh` if available; gracefully returns
// "powershell not available" when neither is present, so the model can fall
// back to the bash tool.
//
// Output is truncated at 30KB (same as the bash tool).
// Timeout is configurable (default 2m, max 10m).
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	// maxPSOutput caps PowerShell output to 30KB.
	maxPSOutput = 30 * 1024
	// defaultPSTimeout is the default PowerShell timeout.
	defaultPSTimeout = 2 * time.Minute
	// maxPSTimeout is the maximum PowerShell timeout.
	maxPSTimeout = 10 * time.Minute
)

// psExecPath returns the path to the best-available PowerShell binary.
// Returns "" when PowerShell is not available on this system.
func psExecPath() string {
	// Prefer pwsh (PowerShell Core) over powershell.exe (Windows PowerShell).
	if path, err := exec.LookPath("pwsh"); err == nil {
		return path
	}
	if runtime.GOOS == "windows" {
		if path, err := exec.LookPath("powershell.exe"); err == nil {
			return path
		}
	}
	return ""
}

// handlePowerShell implements the powershell tool (T-R1P-015).
func (r *Registry) handlePowerShell(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"` // milliseconds
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("command is required")
	}

	psPath := psExecPath()
	if psPath == "" {
		return "powershell: not available on this system. Use the bash tool for shell commands, or install PowerShell Core (https://github.com/PowerShell/PowerShell).", nil
	}

	timeout := defaultPSTimeout
	if args.Timeout > 0 {
		timeout = time.Duration(args.Timeout) * time.Millisecond
		if timeout > maxPSTimeout {
			timeout = maxPSTimeout
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, psPath, "-NonInteractive", "-Command", args.Command) // #nosec G204 — binary resolved via LookPath; command is from Stoke-internal orchestration
	cmd.Dir = r.workDir
	out, err := cmd.CombinedOutput()

	outStr := string(out)
	if len(outStr) > maxPSOutput {
		mid := maxPSOutput / 2
		outStr = outStr[:mid] + "\n...(middle truncated)...\n" + outStr[len(outStr)-mid:]
	}

	if err != nil {
		exitStr := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitStr = fmt.Sprintf("\nexit_code: %d", exitErr.ExitCode())
		}
		return fmt.Sprintf("powershell output:\n%s%s\nerror: %v", outStr, exitStr, err), nil
	}
	return fmt.Sprintf("powershell output:\n%s", outStr), nil
}
