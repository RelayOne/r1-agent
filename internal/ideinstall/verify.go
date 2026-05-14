package ideinstall

// verify.go — `r1 ide verify` report (spec §T8). Returns one record
// per IDE with three fields: Found (is the IDE installed?),
// Registered (is R1 listed in the config?), Path (where R1 looked).

import (
	"fmt"
	"io"
	"os"
)

// VerifyStatus enumerates the three valid status words the verify
// table prints per spec §T8.2.
type VerifyStatus string

const (
	VerifyStatusRegistered   VerifyStatus = "registered"
	VerifyStatusUnregistered VerifyStatus = "unregistered"
	VerifyStatusNotFound     VerifyStatus = "not-found"
)

// VerifyResult is one row of the `r1 ide verify` report.
type VerifyResult struct {
	IDE        string
	Path       string
	Found      bool
	Registered bool
	Status     VerifyStatus
	Err        error
}

// Verify probes every supported IDE and returns one VerifyResult per
// IDE in the order specified by SupportedIDEs (cursor, windsurf,
// vscode, jetbrains). Per spec §T8.2 verify is a report, not a gate
// — it never returns a non-nil error itself, but per-IDE Err fields
// surface I/O errors so callers can render them.
func Verify() []VerifyResult {
	out := make([]VerifyResult, 0, len(SupportedIDEs))
	for _, ide := range SupportedIDEs {
		out = append(out, verifyOne(ide))
	}
	return out
}

// verifyOne dispatches to the right detector + registration probe.
// Each branch is intentionally explicit (not table-driven) because
// JetBrains uses a filesystem probe instead of a JSON parse.
func verifyOne(ide string) VerifyResult {
	res := VerifyResult{IDE: ide, Status: VerifyStatusNotFound}
	switch ide {
	case IDECursor:
		// Workspace-scope is the install default; prefer the
		// workspace path when both are populated so the verify
		// output matches what the operator just installed.
		d := cursorDetector{}
		if path, _, err := d.DetectConfig(ScopeWorkspace, ""); err == nil {
			if reg, _ := IsRegistered(path, CursorRootKey, R1EntryKey); reg {
				res.Path = path
				res.Found = true
				res.Registered = true
				res.Status = VerifyStatusRegistered
				return res
			}
		}
		path, found, err := d.DetectConfig(ScopeGlobal, "")
		res.Path = path
		res.Found = found
		res.Err = err
		reg, regErr := IsRegistered(path, CursorRootKey, R1EntryKey)
		if regErr != nil && res.Err == nil {
			res.Err = regErr
		}
		res.Registered = reg
		res.Status = classifyStatus(found, reg)

	case IDEWindsurf:
		d := windsurfDetector{}
		path, found, err := d.DetectConfig(ScopeGlobal, "")
		res.Path = path
		res.Found = found
		res.Err = err
		reg, regErr := IsRegistered(path, CursorRootKey, R1EntryKey)
		if regErr != nil && res.Err == nil {
			res.Err = regErr
		}
		res.Registered = reg
		res.Status = classifyStatus(found, reg)

	case IDEVSCode:
		d := vscodeDetector{}
		if path, _, err := d.DetectConfig(ScopeWorkspace, ""); err == nil {
			if reg, _ := IsRegistered(path, VSCodeRootKey, R1EntryKey); reg {
				res.Path = path
				res.Found = true
				res.Registered = true
				res.Status = VerifyStatusRegistered
				return res
			}
		}
		path, found, err := d.DetectConfig(ScopeGlobal, "")
		res.Path = path
		res.Found = found
		res.Err = err
		reg, regErr := IsRegistered(path, VSCodeRootKey, R1EntryKey)
		if regErr != nil && res.Err == nil {
			res.Err = regErr
		}
		res.Registered = reg
		res.Status = classifyStatus(found, reg)

	case IDEJetBrains:
		reg, pluginsDir, err := IsJetBrainsRegistered("IntelliJIdea", "")
		res.Path = pluginsDir
		_, statErr := os.Stat(pluginsDir)
		found := statErr == nil
		res.Found = found
		res.Registered = reg
		if err != nil {
			res.Err = err
		}
		res.Status = classifyStatus(found, reg)
	}
	return res
}

func classifyStatus(found, registered bool) VerifyStatus {
	switch {
	case registered:
		return VerifyStatusRegistered
	case found:
		return VerifyStatusUnregistered
	default:
		return VerifyStatusNotFound
	}
}

// PrintVerifyTable renders the verify report to w in the format
// described in spec §T8.2: one line per IDE, columns separated by
// pipes, IDE name padded to 9 chars and status padded to 12 chars
// so the pipes align across rows.
func PrintVerifyTable(w io.Writer, rows []VerifyResult) {
	for _, r := range rows {
		fmt.Fprintf(w, "%-9s | %-12s | %s\n", r.IDE, r.Status, r.Path)
	}
}

// Uninstall dispatches to the right per-IDE uninstaller. Used by
// `r1 ide uninstall <ide>`.
func Uninstall(ide string) (Result, error) {
	switch ide {
	case IDECursor:
		return UninstallCursor()
	case IDEWindsurf:
		return UninstallWindsurf()
	case IDEVSCode:
		return UninstallVSCode()
	case IDEJetBrains:
		return UninstallJetBrains("IntelliJIdea", "")
	default:
		return Result{}, fmt.Errorf("unknown ide %q", ide)
	}
}
