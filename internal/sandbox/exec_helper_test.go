package sandbox

import (
	"os"
	"strings"
	"testing"
)

// TestMain doubles this test binary as the __sandbox-exec helper: the
// Landlock wrapper re-execs os.Executable(), which under `go test` IS this
// binary. Intercepting before m.Run gives the integration tests a real
// re-exec target without building cmd/r1.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperSubcommand {
		os.Exit(RunExecHelper(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

// All failure paths must return helperExitCode WITHOUT executing anything.
func TestRunExecHelperFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{"no args", nil, "missing --policy"},
		{"policy without value", []string{"--policy"}, "--policy requires a value"},
		{"missing argv", []string{"--policy", "{}"}, "missing payload argv"},
		{"empty argv after separator", []string{"--policy", "{}", "--"}, "missing payload argv"},
		{"unknown flag", []string{"--nope"}, "unknown argument"},
		{"malformed policy json", []string{"--policy", "{not json", "--", "bash", "-c", "true"}, "malformed policy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			code := RunExecHelper(tc.args, &sb)
			if code != helperExitCode {
				t.Errorf("exit code = %d, want %d", code, helperExitCode)
			}
			if !strings.Contains(sb.String(), tc.wantMsg) {
				t.Errorf("stderr %q missing %q", sb.String(), tc.wantMsg)
			}
		})
	}
}
