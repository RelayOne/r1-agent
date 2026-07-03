package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
)

// When the OS sandbox is engaged, cron_create and notebook_cell_run must be
// denied: both exec on the host OUTSIDE the bash containment, so allowing
// them would be a silent escape hatch. Denying is the fail-closed choice.
func TestSandboxDeniesHostExecTools(t *testing.T) {
	cronIn, _ := json.Marshal(map[string]any{"id": "x", "schedule": "* * * * *", "command": "echo hi"})
	nbIn, _ := json.Marshal(map[string]any{"path": "nb.ipynb", "source": "print(1)"})

	cases := []struct {
		name    string
		call    func(r *Registry) (string, error)
		wantSub string
	}{
		{"cron_create denied", func(r *Registry) (string, error) {
			return r.handleCronCreate(context.Background(), cronIn)
		}, "cron_create is disabled"},
		{"notebook_cell_run denied", func(r *Registry) (string, error) {
			return r.handleNotebookCellRun(context.Background(), nbIn)
		}, "notebook_cell_run is disabled"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry(t.TempDir())
			r.sbx = &stubWrapper{}
			_, err := tc.call(r)
			if err == nil {
				t.Fatalf("%s must return an error under sandbox", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q missing %q", err, tc.wantSub)
			}
		})
	}
}

// Without a sandbox the tools are NOT denied (they degrade gracefully on
// their own — e.g. jupyter/crontab absent — but must not be blocked here).
func TestNoSandboxDoesNotDenyHostExecTools(t *testing.T) {
	r := NewRegistry(t.TempDir())
	nbIn, _ := json.Marshal(map[string]any{"path": "nb.ipynb", "source": "print(1)"})
	out, err := r.handleNotebookCellRun(context.Background(), nbIn)
	if err != nil && strings.Contains(err.Error(), "disabled") {
		t.Errorf("notebook_cell_run must not be sandbox-denied without a sandbox: %v", err)
	}
	// With no jupyter installed the handler returns a graceful message, not
	// the sandbox-deny error.
	if strings.Contains(out, "is disabled while the native OS sandbox") {
		t.Errorf("unexpected sandbox-deny output without sandbox: %q", out)
	}
}

// Definitions must drop the two host-exec tools when the sandbox is engaged,
// and keep them otherwise.
func TestDefinitionsFilterHostExecUnderSandbox(t *testing.T) {
	has := func(defs []provider.ToolDef, name string) bool {
		for _, d := range defs {
			if d.Name == name {
				return true
			}
		}
		return false
	}

	r := NewRegistry(t.TempDir())
	if defs := r.Definitions(); !has(defs, "cron_create") || !has(defs, "notebook_cell_run") {
		t.Fatal("host-exec tools should be advertised without a sandbox")
	}

	r.sbx = &stubWrapper{}
	defs := r.Definitions()
	if has(defs, "cron_create") {
		t.Error("cron_create must be dropped from Definitions under sandbox")
	}
	if has(defs, "notebook_cell_run") {
		t.Error("notebook_cell_run must be dropped from Definitions under sandbox")
	}
	// Sibling read-only tools stay available.
	if !has(defs, "notebook_read") || !has(defs, "cron_list") {
		t.Error("read-only siblings (notebook_read/cron_list) must remain under sandbox")
	}
}
