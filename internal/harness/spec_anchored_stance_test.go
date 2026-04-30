package harness_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/harness"
)

func TestSpecAnchoredStanceValidateCommitMsg(t *testing.T) {
	t.Parallel()

	specPath := filepath.Join(t.TempDir(), "task-spec.md")
	specBody := "" +
		"# Task\n" +
		"Implement a memory drift validator before session start.\n" +
		"Add branch checks for deleted remotes.\n"
	if err := os.WriteFile(specPath, []byte(specBody), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	tests := []struct {
		name    string
		msg     string
		wantErr bool
	}{
		{
			name:    "accepts verbatim spec citation",
			msg:     "feat: add reconciler\n\nSpec: Implement a memory drift validator before session start.",
			wantErr: false,
		},
		{
			name:    "rejects message without spec citation",
			msg:     "feat: add reconciler\n\nImplemented the reconciler quickly.",
			wantErr: true,
		},
		{
			name:    "accepts direct pasted line without prefix",
			msg:     "feat: add reconciler\n\nAdd branch checks for deleted remotes.",
			wantErr: false,
		},
	}

	stance := harness.SpecAnchoredStance{SpecPath: specPath}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := stance.ValidateCommitMsg(tc.msg)
			if tc.wantErr && err == nil {
				t.Fatal("ValidateCommitMsg returned nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateCommitMsg returned error: %v", err)
			}
		})
	}
}
