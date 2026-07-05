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

func TestBashBreakerErrorNamesReason(t *testing.T) {
	err := bashBreakerCheck("rm -rf " + "/")
	if err == nil || !strings.Contains(err.Error(), "harness breaker") {
		t.Errorf("error should name the harness breaker: %v", err)
	}
}
