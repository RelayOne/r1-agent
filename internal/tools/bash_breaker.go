package tools

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// SOTA gap #7: hard destructive-command breakers on the native tool path.
//
// The Bash tool handler execs `bash -c <command>` directly. The policy
// gate (internal/engine/policy_gate.go) is the configurable deny/ask/allow
// layer, but it fails OPEN when no policy client is wired (single-user
// installs, `r1 chat` without a policy file) — leaving the model's shell
// access ungated. These breakers are a small, harness-enforced,
// always-on floor that runs before exec regardless of policy config: they
// block only unambiguously catastrophic commands (recursive delete of /
// or $HOME, disk overwrite, filesystem format, fork bomb) so they never
// false-positive on real build/test commands. They are compound-aware —
// `safe && rm -rf /` is blocked on the dangerous segment — and strip
// common wrappers (sudo/timeout/nice/env/xargs) so a wrapper cannot hide
// the payload.

var (
	// Raw device / disk destruction.
	reDdDisk = regexp.MustCompile(`\bdd\b[^|;&]*\bof=/dev/(sd|nvme|vd|hd|disk|mmcblk)`)
	reMkfs   = regexp.MustCompile(`\bmkfs(\.\w+)?\b`)
	reWipefs = regexp.MustCompile(`\bwipefs\b`)
	reRedirDisk = regexp.MustCompile(`>\s*/dev/(sd|nvme|vd|hd|disk|mmcblk)`)
	// Classic fork bomb.
	reForkBomb = regexp.MustCompile(`:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`)
	// A single operand that names a filesystem root or the home directory
	// itself (bare `/`, `~`, `$HOME`, `${HOME}`, or a path directly under
	// them like `~/` or `$HOME/x`). Deliberately does NOT match specific
	// absolute subtrees such as `/tmp/build` or `/home/eric/proj` — the
	// always-on floor only fires on the catastrophic root/home targets so it
	// never false-positives on real build/test cleanup.
	reRootHomeTarget = regexp.MustCompile(`^(/|~|\$HOME|\$\{HOME\})(/.*)?$`)
)

// stripQuotes removes a single layer of surrounding matching single or
// double quotes from an operand, so a quoted catastrophic target like
// `"/"` or `'/'` is compared by the value the shell would use, not the
// quoted literal (which would slip past an anchored target match).
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// isAssignment reports whether tok is a leading `NAME=VALUE` shell
// assignment (applied as a one-command env var, e.g. `FOO=bar rm -rf /`),
// so the command word underneath can be recognized.
func isAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for j, c := range tok[:eq] {
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case j > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// commandWord returns the effective command name and its remaining
// argument tokens. It skips leading NAME=VALUE assignments, strips a
// leading backslash (`\rm`, used to bypass a shell alias), and reduces an
// absolute/relative path to its basename (`/bin/rm`, `/usr/bin/chmod`) so
// the breaker recognizes the command however it is spelled — the previous
// literal-first-token check let `/bin/rm -rf /` and `\rm -rf /` through.
func commandWord(fields []string) (string, []string) {
	i := 0
	for i < len(fields) && isAssignment(fields[i]) {
		i++
	}
	if i >= len(fields) {
		return "", nil
	}
	raw := strings.TrimPrefix(fields[i], `\`)
	return path.Base(raw), fields[i+1:]
}

// isRootHomeTarget reports whether an operand names a filesystem root or
// the home directory (or a glob/quote spelling of one) — the only targets
// the always-on floor fires on. It unquotes the operand and treats the
// catastrophic globs `/*`, `/.`, `/home/*` and the home-content globs
// `~/*`, `$HOME/*` as root/home, while deliberately NOT matching arbitrary
// absolute subtrees (`/tmp/build`, `/home/eric/proj`) or relative globs
// (`mydir/*`) so real cleanup commands never false-positive.
func isRootHomeTarget(op string) bool {
	op = stripQuotes(op)
	switch op {
	case "/", "/*", "/.", "/home", "/home/*", "/home/.":
		return true
	}
	return reRootHomeTarget.MatchString(op)
}

// rmDeletesRootHome reports whether a single command segment is a recursive
// force `rm` of a filesystem root or the home directory. Flags are parsed as
// independent booleans regardless of order or fusion, so `rm -rf /`,
// `rm -r -f /`, `rm -f -r ~`, and `rm --recursive --force /` are all caught —
// the old regex only matched r-before-f fused in a single flag token.
func rmDeletesRootHome(seg string) bool {
	cmd, args := commandWord(strings.Fields(seg))
	if cmd != "rm" {
		return false
	}
	fields := append([]string{"rm"}, args...)
	rec, force := false, false
	endFlags := false
	var operands []string
	for _, t := range fields[1:] {
		if endFlags {
			operands = append(operands, t)
			continue
		}
		switch {
		case t == "--":
			endFlags = true
		case strings.HasPrefix(t, "--"):
			name := strings.TrimPrefix(t, "--")
			if i := strings.IndexByte(name, '='); i >= 0 {
				name = name[:i]
			}
			switch name {
			case "recursive":
				rec = true
			case "force":
				force = true
			}
		case strings.HasPrefix(t, "-") && len(t) > 1:
			for _, c := range t[1:] {
				switch c {
				case 'r', 'R':
					rec = true
				case 'f':
					force = true
				}
			}
		default:
			operands = append(operands, t)
		}
	}
	if !rec || !force {
		return false
	}
	for _, op := range operands {
		if isRootHomeTarget(op) {
			return true
		}
	}
	return false
}

// chmodRecursiveRoot reports whether a segment recursively changes
// permissions/ownership on a filesystem root or the home directory. Like
// rmDeletesRootHome, the recursive flag and the target operand are matched
// independent of order, so `chmod 777 -R /` (flag after the mode) is caught.
func chmodRecursiveRoot(seg string) bool {
	cmd, args := commandWord(strings.Fields(seg))
	switch cmd {
	case "chmod", "chown", "chgrp":
	default:
		return false
	}
	fields := append([]string{cmd}, args...)
	rec := false
	endFlags := false
	var operands []string
	for _, t := range fields[1:] {
		if endFlags {
			operands = append(operands, t)
			continue
		}
		switch {
		case t == "--":
			endFlags = true
		case strings.HasPrefix(t, "--"):
			name := strings.TrimPrefix(t, "--")
			if i := strings.IndexByte(name, '='); i >= 0 {
				name = name[:i]
			}
			if name == "recursive" {
				rec = true
			}
		case strings.HasPrefix(t, "-") && len(t) > 1:
			for _, c := range t[1:] {
				if c == 'r' || c == 'R' {
					rec = true
				}
			}
		default:
			operands = append(operands, t)
		}
	}
	if !rec {
		return false
	}
	for _, op := range operands {
		if isRootHomeTarget(op) {
			return true
		}
	}
	return false
}

// bashBreakerCheck returns a non-nil error naming the reason when command
// contains an unambiguously catastrophic operation. It splits compound
// commands on &&, ||, |, ;, and newlines and checks each segment after
// stripping leading wrappers, so a dangerous subcommand cannot ride in on
// a safe prefix or a wrapper.
func bashBreakerCheck(command string) error {
	// The fork bomb is defined BY the separators splitCompound would split
	// on (`|`, `&`, `;`), so match it against the whole command first.
	if reForkBomb.MatchString(command) {
		return fmt.Errorf("blocked by harness breaker: fork bomb (%q)", command)
	}
	for _, seg := range splitCompound(command) {
		seg = stripWrappers(strings.TrimSpace(seg))
		if seg == "" {
			continue
		}
		switch {
		case rmDeletesRootHome(seg):
			return fmt.Errorf("blocked by harness breaker: recursive force-delete of a filesystem root or home directory (%q)", seg)
		case reDdDisk.MatchString(seg) || reRedirDisk.MatchString(seg):
			return fmt.Errorf("blocked by harness breaker: raw write to a block device (%q)", seg)
		case reMkfs.MatchString(seg) || reWipefs.MatchString(seg):
			return fmt.Errorf("blocked by harness breaker: filesystem format/wipe (%q)", seg)
		case chmodRecursiveRoot(seg):
			return fmt.Errorf("blocked by harness breaker: recursive permission/ownership change on a filesystem root (%q)", seg)
		}
	}
	return nil
}

// splitCompound breaks a shell command into the segments a shell would
// run sequentially/piped, so each is breaker-checked independently. It is
// a deliberately simple splitter on &&, ||, |, ;, and newlines — it is not
// a full shell parser, and it errs toward OVER-splitting (more segments
// checked), which is the safe direction for a deny-only floor.
func splitCompound(command string) []string {
	// Normalize the multi-char operators to single-byte sentinels so a
	// single Split pass handles them all.
	replacer := strings.NewReplacer("&&", "\x00", "||", "\x00", "|", "\x00", ";", "\x00", "\n", "\x00", "&", "\x00")
	return strings.Split(replacer.Replace(command), "\x00")
}

// wrapperPrefixes are command prefixes that pass their remaining arguments
// through to another command; stripping them exposes the real payload to
// the breaker (e.g. `sudo rm -rf /`, `timeout 5 dd of=/dev/sda`).
var wrapperPrefixes = map[string]bool{
	"sudo": true, "nice": true, "ionice": true, "nohup": true,
	"time": true, "timeout": true, "stdbuf": true, "env": true,
	"xargs": true, "command": true, "builtin": true, "eval": true,
}

// stripWrappers removes leading wrapper commands and their obvious option/
// argument tokens (best-effort) so the breaker sees the underlying
// command. Conservative: it only strips known wrappers and stops at the
// first token that is not a wrapper, a flag, or a wrapper argument.
func stripWrappers(seg string) string {
	fields := strings.Fields(seg)
	for len(fields) > 0 {
		head := fields[0]
		if !wrapperPrefixes[head] {
			break
		}
		fields = fields[1:]
		// Skip the wrapper's own leading options and one numeric/kv arg
		// (e.g. `timeout 5`, `env FOO=bar`) so the next real token surfaces.
		for len(fields) > 0 {
			f := fields[0]
			if strings.HasPrefix(f, "-") || isNumeric(f) || strings.Contains(f, "=") {
				fields = fields[1:]
				continue
			}
			break
		}
	}
	return strings.Join(fields, " ")
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' && c != 's' && c != 'm' && c != 'h' {
			return false
		}
	}
	return true
}
