package tools

import (
	"fmt"
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
	// Recursive force-delete of a filesystem root or the home directory.
	reRmRootHome = regexp.MustCompile(`\brm\s+(-[a-zA-Z]*\s+)*-[a-zA-Z]*[rR][a-zA-Z]*f[a-zA-Z]*\s+.*(/|~|\$HOME|\$\{HOME\})(\s|/|$)`)
	reRmRfShort  = regexp.MustCompile(`\brm\s+-[a-zA-Z]*f[a-zA-Z]*[rR][a-zA-Z]*\s+.*(/|~|\$HOME|\$\{HOME\})(\s|/|$)`)
	// Raw device / disk destruction.
	reDdDisk = regexp.MustCompile(`\bdd\b[^|;&]*\bof=/dev/(sd|nvme|vd|hd|disk|mmcblk)`)
	reMkfs   = regexp.MustCompile(`\bmkfs(\.\w+)?\b`)
	reWipefs = regexp.MustCompile(`\bwipefs\b`)
	reRedirDisk = regexp.MustCompile(`>\s*/dev/(sd|nvme|vd|hd|disk|mmcblk)`)
	// Classic fork bomb.
	reForkBomb = regexp.MustCompile(`:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`)
	// chmod/chown -R against a filesystem root.
	reChmodRoot = regexp.MustCompile(`\bch(mod|own)\s+(-[a-zA-Z]*\s+)*-[a-zA-Z]*[rR]\S*\s+\S+\s+(/|~|\$HOME)(\s|$)`)
)

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
		case reRmRootHome.MatchString(seg) || reRmRfShort.MatchString(seg):
			return fmt.Errorf("blocked by harness breaker: recursive force-delete of a filesystem root or home directory (%q)", seg)
		case reDdDisk.MatchString(seg) || reRedirDisk.MatchString(seg):
			return fmt.Errorf("blocked by harness breaker: raw write to a block device (%q)", seg)
		case reMkfs.MatchString(seg) || reWipefs.MatchString(seg):
			return fmt.Errorf("blocked by harness breaker: filesystem format/wipe (%q)", seg)
		case reChmodRoot.MatchString(seg):
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
