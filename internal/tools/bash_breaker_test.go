package tools

import (
	"strings"
	"testing"
)

func TestBashBreakerBlocksCatastrophes(t *testing.T) {
	slash := "/"
	home := "~"
	blocked := []string{
		"rm -rf " + slash,
		"rm -rf " + home,
		"rm -rf $HOME",
		"rm -fr " + slash,
		"sudo rm -rf " + slash,
		"timeout 5 rm -rf " + slash,
		"ls && rm -rf " + slash,          // compound: dangerous 2nd segment
		"echo hi ; rm -rf ~/",            // compound with semicolon
		"dd if=/dev/zero of=/dev/sda",
		"mkfs.ext4 /dev/sdb1",
		":(){ :|:& };:",                  // fork bomb
		"chmod -R 000 " + slash,
		"cat x > /dev/sda",
		// SECURITY gap #1: split / long / reordered flags must not bypass
		// the destructive floor (old regex required r-before-f fused).
		"rm -r -f " + slash,               // split short flags
		"rm -f -r " + slash,               // split, force first
		"rm --recursive --force " + slash, // long flags
		"rm --force --recursive " + home,  // long flags, reordered
		"rm -r -f " + home,                // split flags on home
		"rm -r -f $HOME",
		"rm -r -f ~/",
		"chmod 777 -R " + slash,           // recursive flag AFTER the mode operand
		"chown -R root:root " + slash,      // recursive ownership change on root
	}
	for _, c := range blocked {
		if err := bashBreakerCheck(c); err == nil {
			t.Errorf("breaker did NOT block: %q", c)
		}
	}
}

func TestBashBreakerAllowsRealCommands(t *testing.T) {
	allowed := []string{
		"go build ./...",
		"go test ./... -count=1",
		"rm -rf ./build", // relative path, not root/home
		"rm -rf node_modules",
		"rm -f coverage.out",
		"npm ci && npm run build",
		"git clean -fdx",
		"find . -name '*.tmp' -delete",
		"docker run --rm -v $PWD:/w img",
		"dd if=input.bin of=output.bin", // file-to-file, not a device
		"chmod -R 755 ./scripts",
		"rm -rf /tmp/mybuild/cache", // under /tmp, not root itself
		// Order-independent parsing must still NOT fire on safe targets.
		"rm -r -f ./build",             // split flags, relative path
		"rm --recursive --force ./dist", // long flags, relative path
		"rm -r -f /tmp/build/x",        // split flags, under /tmp
		"rm --recursive ./node_modules", // recursive but not forced, safe target
		"chmod 777 -R ./scripts",       // reordered flag, safe target
	}
	for _, c := range allowed {
		if err := bashBreakerCheck(c); err != nil {
			t.Errorf("breaker FALSE-POSITIVE on safe command %q: %v", c, err)
		}
	}
}

// TestBashBreakerNoPathQuoteGlobBypass pins the gap-fix-review vectors: the
// field-parser rewrite must resolve the command by basename (not literal
// first token), skip leading NAME=VALUE assignments, and unquote/glob-match
// the target — otherwise /bin/rm, \rm, FOO=bar rm, quoted "/" and /* all
// bypassed the always-on floor.
func TestBashBreakerNoPathQuoteGlobBypass(t *testing.T) {
	slash := "/"
	blocked := []string{
		"/bin/rm -rf " + slash,           // absolute path to rm
		"/usr/bin/rm -rf " + slash,       // another absolute path
		`\rm -rf ` + slash,               // backslash-escaped (alias bypass)
		"FOO=bar rm -rf " + slash,        // leading env assignment
		"A=1 B=2 rm -rf " + slash,        // multiple assignments
		`rm -rf "` + slash + `"`,         // double-quoted root
		"rm -rf '" + slash + "'",         // single-quoted root
		"rm -rf /*",                      // root glob
		"rm -rf /home/*",                 // all-homes glob
		"/bin/chmod -R 777 " + slash,     // absolute path to chmod
		"FOO=x chmod -R " + slash,        // assignment + chmod
		"/bin/rm -r -f /home",            // path rm of /home
	}
	for _, c := range blocked {
		if bashBreakerCheck(c) == nil {
			t.Errorf("bypass NOT blocked: %q", c)
		}
	}
	allowed := []string{
		"rm -rf mydir/*",        // relative glob
		"rm -rf /tmp/*",         // /tmp glob, not root
		"rm -rf /home/eric/proj", // a specific user subtree
		"/bin/rm -rf ./build",   // absolute rm on a relative target
		"cat CLAUDE.md",         // a read, no delete
	}
	for _, c := range allowed {
		if err := bashBreakerCheck(c); err != nil {
			t.Errorf("false-positive on safe command %q: %v", c, err)
		}
	}
}

func TestBashBreakerErrorNamesReason(t *testing.T) {
	err := bashBreakerCheck("rm -rf " + "/")
	if err == nil || !strings.Contains(err.Error(), "harness breaker") {
		t.Errorf("error should name the harness breaker: %v", err)
	}
}
