package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hasSeq reports whether want appears as a contiguous subsequence of args.
func hasSeq(args []string, want ...string) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j := range want {
			if args[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func seqIndex(args []string, want ...string) int {
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j := range want {
			if args[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func TestBwrapArgs(t *testing.T) {
	work := t.TempDir()
	denyDir := t.TempDir()
	denyFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(denyFile, []byte("SECRET=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	roDir := t.TempDir()
	cacheDir := t.TempDir()

	cases := []struct {
		name    string
		policy  Policy
		wantSeq [][]string
		banSeq  [][]string
	}{
		{
			name:   "baseline flags always present",
			policy: Policy{AllowEgress: true},
			wantSeq: [][]string{
				{"--die-with-parent"},
				{"--new-session"},
				{"--unshare-pid"},
				{"--ro-bind", "/", "/"},
				{"--dev", "/dev"},
				{"--proc", "/proc"},
				{"--tmpfs", "/tmp"},
				{"--chdir", work},
				{"--", "bash", "-c", "echo hi"},
			},
			banSeq: [][]string{{"--unshare-net"}},
		},
		{
			name:    "egress deny adds unshare-net",
			policy:  Policy{AllowEgress: false},
			wantSeq: [][]string{{"--unshare-net"}},
		},
		{
			name:    "allow-write dir gets rw bind",
			policy:  Policy{AllowEgress: true, AllowWrite: []string{work}},
			wantSeq: [][]string{{"--bind", work, work}},
		},
		{
			name:    "write caches get rw binds",
			policy:  Policy{AllowEgress: true, WriteCaches: []string{cacheDir}},
			wantSeq: [][]string{{"--bind", cacheDir, cacheDir}},
		},
		{
			name:    "allow-read dir gets ro bind",
			policy:  Policy{AllowEgress: true, AllowRead: []string{roDir}},
			wantSeq: [][]string{{"--ro-bind", roDir, roDir}},
		},
		{
			name:    "deny-read dir masked with tmpfs",
			policy:  Policy{AllowEgress: true, DenyRead: []string{denyDir}},
			wantSeq: [][]string{{"--tmpfs", denyDir}},
		},
		{
			name:    "deny-read file masked with dev-null bind",
			policy:  Policy{AllowEgress: true, DenyRead: []string{denyFile}},
			wantSeq: [][]string{{"--ro-bind", "/dev/null", denyFile}},
		},
		{
			name:   "nonexistent entries skipped without error",
			policy: Policy{AllowEgress: true, DenyRead: []string{"/no/such/path"}, AllowWrite: []string{"/also/no/such"}},
			banSeq: [][]string{
				{"--tmpfs", "/no/such/path"},
				{"--bind", "/also/no/such", "/also/no/such"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := bwrapArgs("echo hi", work, tc.policy)
			for _, seq := range tc.wantSeq {
				if !hasSeq(args, seq...) {
					t.Errorf("argv missing %v\nargs: %v", seq, args)
				}
			}
			for _, seq := range tc.banSeq {
				if hasSeq(args, seq...) {
					t.Errorf("argv must not contain %v\nargs: %v", seq, args)
				}
			}
		})
	}
}

// TestBwrapArgsMaskOrdering pins the mount ordering invariant: a DenyRead
// mask nested inside an AllowWrite bind must be emitted AFTER the bind,
// or the bind would shadow the mask and the secret would be readable.
func TestBwrapArgsMaskOrdering(t *testing.T) {
	work := t.TempDir()
	secret := filepath.Join(work, ".env")
	if err := os.WriteFile(secret, []byte("SECRET=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := bwrapArgs("cat .env", work, Policy{
		AllowEgress: true,
		AllowWrite:  []string{work},
		DenyRead:    []string{secret},
	})
	bindIdx := seqIndex(args, "--bind", work, work)
	maskIdx := seqIndex(args, "--ro-bind", "/dev/null", secret)
	if bindIdx < 0 || maskIdx < 0 {
		t.Fatalf("expected both bind and mask in argv: %v", args)
	}
	if maskIdx < bindIdx {
		t.Errorf("mask at %d must come after enclosing bind at %d\nargs: %v", maskIdx, bindIdx, args)
	}
}

func TestBwrapAvailable_NoBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	w := &bwrapWrapper{}
	err := w.Available(Policy{})
	if err == nil {
		t.Fatal("Available must fail when bwrap is not on PATH")
	}
	if !strings.Contains(err.Error(), "bwrap") {
		t.Errorf("error should name bwrap: %v", err)
	}
}
