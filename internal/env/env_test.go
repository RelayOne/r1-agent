package env

import (
	"testing"
)

func TestExecResultSuccess(t *testing.T) {
	r := &ExecResult{ExitCode: 0, Stdout: "ok"}
	if !r.Success() {
		t.Error("exit 0 should be success")
	}

	r2 := &ExecResult{ExitCode: 1, Stdout: "fail"}
	if r2.Success() {
		t.Error("exit 1 should not be success")
	}
}

func TestExecResultCombinedOutput(t *testing.T) {
	r := &ExecResult{Stdout: "out", Stderr: "err"}
	got := r.CombinedOutput()
	if got != "out\nerr" {
		t.Errorf("combined=%q, want %q", got, "out\nerr")
	}

	r2 := &ExecResult{Stdout: "out"}
	if r2.CombinedOutput() != "out" {
		t.Errorf("combined without stderr=%q, want %q", r2.CombinedOutput(), "out")
	}
}

func TestServiceAddrURL(t *testing.T) {
	s := ServiceAddr{Protocol: "http", Host: "localhost", Port: 8080}
	if s.URL() != "http://localhost:8080" {
		t.Errorf("url=%q", s.URL())
	}

	s2 := ServiceAddr{Host: "db", Port: 5432}
	if s2.URL() != "http://db:5432" {
		t.Errorf("url with default proto=%q", s2.URL())
	}
}

func TestBackendConstants(t *testing.T) {
	backends := []Backend{BackendInProc, BackendDocker, BackendSSH, BackendFly, BackendEmber}
	seen := make(map[Backend]bool)
	for _, b := range backends {
		if seen[b] {
			t.Errorf("duplicate backend: %s", b)
		}
		seen[b] = true
		if string(b) == "" {
			t.Error("empty backend constant")
		}
	}
}
