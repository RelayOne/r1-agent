package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1-agent/internal/plan"
)

// TestWriteUpstreamSessionMarker_S33_DualEmitsStokeAndR1Version
// verifies §S3-3 of work-r1-rename.md: the SessionProvenance block
// persisted in the session marker JSON carries BOTH the legacy
// `stoke_version` key AND the canonical `r1_version` key with the
// identical value during the 30-day rename window. The test also
// covers the reverse direction (caller sets only R1Version) to
// confirm forward-compat.
func TestWriteUpstreamSessionMarker_S33_DualEmitsStokeAndR1Version(t *testing.T) {
	cases := []struct {
		name     string
		prov     *SessionProvenance
		wantVers string
	}{
		{
			name: "LegacyOnly_MirrorsToR1",
			prov: &SessionProvenance{
				WorkerModel:  "claude-sonnet-4-7",
				StokeVersion: "0.42.1",
			},
			wantVers: "0.42.1",
		},
		{
			name: "CanonicalOnly_MirrorsToStoke",
			prov: &SessionProvenance{
				WorkerModel: "claude-sonnet-4-7",
				R1Version:   "1.0.0",
			},
			wantVers: "1.0.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			sess := plan.Session{
				ID:    "sess-dualkey-" + tc.name,
				Title: "S3-3 dual-key smoke",
			}

			if err := writeUpstreamSessionMarker(repoRoot, sess, nil, "dual-key-test", tc.prov); err != nil {
				t.Fatalf("writeUpstreamSessionMarker: %v", err)
			}

			path := filepath.Join(repoRoot, ".stoke", "sow-state-markers", sess.ID+".json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read marker: %v", err)
			}

			// Peek at the raw JSON so we assert on the wire-level
			// key names, not on the Go-struct field shape.
			var outer map[string]any
			if err := json.Unmarshal(raw, &outer); err != nil {
				t.Fatalf("unmarshal marker: %v", err)
			}
			provBlock, ok := outer["provenance"].(map[string]any)
			if !ok {
				t.Fatalf("provenance block missing or wrong type in %s", raw)
			}
			for _, k := range []string{"stoke_version", "r1_version"} {
				got, present := provBlock[k]
				if !present {
					t.Errorf("key %q missing in provenance block: %s", k, raw)
					continue
				}
				if s, _ := got.(string); s != tc.wantVers {
					t.Errorf("key %q = %q, want %q", k, s, tc.wantVers)
				}
			}
		})
	}
}
