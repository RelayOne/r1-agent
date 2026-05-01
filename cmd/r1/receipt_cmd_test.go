package main

// receipt_cmd_test.go — regression tests for the schema-version
// downgrade attack closed by PR #24 HIGH-3.
//
// The attack: an anchor was signed under SchemaVersion 2 (Hash composed
// over PrevHash || MerkleRoot || IntervalStart || IntervalEnd). The
// attacker rewrites the anchor's interval_start, then either deletes
// or nulls the schema_version field. The naive verifier sees
// SchemaVersion==0, falls through to v1 (which doesn't bind
// IntervalStart), recomputes a v1-shaped Hash that happens to equal
// the original v2 Hash by collision-with-itself — and reports OK.
//
// The fix: schemaVersionPresence() does a pre-pass over the raw JSON
// so the verifier can distinguish the three "looks like 0" variants
// (missing / "schema_version":null / "schema_version":0). Only the
// genuinely-missing case falls through to v1 legacy compat; the other
// two surface as a tamper rejection.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

// makeV2Anchor builds a structurally valid v2 anchor with a Hash that
// matches its declared composition.
func makeV2Anchor(start, end time.Time, prev, merkle string) ledger.Anchor {
	a := ledger.Anchor{
		SchemaVersion: 2,
		Seq:           0,
		IntervalStart: start.UTC(),
		IntervalEnd:   end.UTC(),
		NodeCount:     1,
		MerkleRoot:    merkle,
		PrevHash:      prev,
	}
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write([]byte(merkle))
	h.Write([]byte(start.UTC().Format(time.RFC3339Nano)))
	h.Write([]byte(end.UTC().Format(time.RFC3339Nano)))
	a.Hash = hex.EncodeToString(h.Sum(nil))
	return a
}

// anchorAsMap marshals the anchor through JSON to a generic map so
// individual tests can model the three "schema_version" tamper variants
// (missing / null / 0) — Go's int zero collapses them in the typed
// Anchor struct, so the only legal source of truth is the raw JSON.
func anchorAsMap(t *testing.T, a ledger.Anchor) map[string]any {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded) == 0 {
		t.Fatalf("anchorAsMap: decoded map is empty")
	}
	return decoded
}

// writeMapAsAnchorJSON marshals a map to receipt.json under dir.
func writeMapAsAnchorJSON(t *testing.T, dir string, m map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, "receipt.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, _ := os.ReadFile(path); len(got) == 0 {
		t.Fatalf("writeMapAsAnchorJSON: empty file at %s", path)
	}
	return path
}

func TestReceiptVerifyAcceptsValidV2(t *testing.T) {
	tmp := t.TempDir()
	start := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	a := makeV2Anchor(start, end, strings.Repeat("0", 64), strings.Repeat("a", 64))
	m := anchorAsMap(t, a)
	path := writeMapAsAnchorJSON(t, tmp, m)

	var stdout, stderr bytes.Buffer
	code := runReceiptVerify([]string{path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("valid v2 anchor: exit=%d, want 0\nstderr=%s", code, stderr.String())
	}
}

// Downgrade attack 1: schema_version field is dropped entirely. Without
// the fix this would fall through to v1 legacy compat AND succeed
// because a tampered IntervalStart isn't part of the v1 hash. We
// model this by mutating IntervalStart so v2 hash no longer matches —
// the verifier must reject because the v1 recompute will not match
// the originally-v2-shaped Hash either.
func TestReceiptVerifyRejectsDowngradeViaMissingField(t *testing.T) {
	tmp := t.TempDir()
	start := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	a := makeV2Anchor(start, end, strings.Repeat("0", 64), strings.Repeat("a", 64))
	// Tamper: rewrite IntervalStart on the in-memory copy. The Hash
	// is still the original (signed) v2 hash.
	a.IntervalStart = start.Add(15 * time.Minute)
	m := anchorAsMap(t, a)
	delete(m, "schema_version")
	if _, present := m["schema_version"]; present {
		t.Fatalf("expected schema_version to be deleted; map=%v", m)
	}
	path := writeMapAsAnchorJSON(t, tmp, m)

	var stdout, stderr bytes.Buffer
	code := runReceiptVerify([]string{path}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("downgrade via missing field accepted: stdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
}

// Downgrade attack 2: schema_version explicit null. Must surface as a
// tamper rejection regardless of the rest of the body.
func TestReceiptVerifyRejectsExplicitNullSchemaVersion(t *testing.T) {
	tmp := t.TempDir()
	start := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	a := makeV2Anchor(start, end, strings.Repeat("0", 64), strings.Repeat("a", 64))
	m := anchorAsMap(t, a)
	m["schema_version"] = nil
	if got, present := m["schema_version"]; !present || got != nil {
		t.Fatalf("expected schema_version=null after tamper; got %v (present=%v)", got, present)
	}
	path := writeMapAsAnchorJSON(t, tmp, m)

	var stdout, stderr bytes.Buffer
	code := runReceiptVerify([]string{path}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("explicit null schema_version accepted: stdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "SCHEMA-VERSION TAMPER") {
		t.Errorf("expected SCHEMA-VERSION TAMPER in stderr, got: %s", stderr.String())
	}
}

// Downgrade attack 3: schema_version explicit 0. Must surface as a
// tamper rejection. (json.Unmarshal collapses this into the same
// in-memory zero as a missing field; only the raw-JSON pre-pass
// distinguishes them.)
func TestReceiptVerifyRejectsExplicitZeroSchemaVersion(t *testing.T) {
	tmp := t.TempDir()
	start := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	a := makeV2Anchor(start, end, strings.Repeat("0", 64), strings.Repeat("a", 64))
	m := anchorAsMap(t, a)
	m["schema_version"] = 0
	// Confirm round-trip preserves the explicit zero. json.Marshal of
	// {0} keeps the field even though Anchor's struct tag has omitempty,
	// because we marshal the raw map, not the typed struct.
	roundtrip, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("roundtrip marshal: %v", err)
	}
	if !strings.Contains(string(roundtrip), `"schema_version":0`) {
		t.Fatalf("expected literal \"schema_version\":0 in JSON, got: %s", roundtrip)
	}
	path := writeMapAsAnchorJSON(t, tmp, m)

	var stdout, stderr bytes.Buffer
	code := runReceiptVerify([]string{path}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("explicit 0 schema_version accepted: stdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "SCHEMA-VERSION TAMPER") {
		t.Errorf("expected SCHEMA-VERSION TAMPER in stderr, got: %s", stderr.String())
	}
}

// Downgrade attack 4: schema_version set to an unknown future value.
// Forward-compat: a build that doesn't know the composition fails closed.
func TestReceiptVerifyRejectsUnknownSchemaVersion(t *testing.T) {
	tmp := t.TempDir()
	start := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	a := makeV2Anchor(start, end, strings.Repeat("0", 64), strings.Repeat("a", 64))
	m := anchorAsMap(t, a)
	m["schema_version"] = 99
	if got := m["schema_version"]; got != 99 {
		t.Fatalf("expected schema_version=99 after tamper; got %v", got)
	}
	path := writeMapAsAnchorJSON(t, tmp, m)

	var stdout, stderr bytes.Buffer
	code := runReceiptVerify([]string{path}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("unknown schema_version accepted: stdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "SCHEMA-VERSION UNKNOWN") {
		t.Errorf("expected SCHEMA-VERSION UNKNOWN in stderr, got: %s", stderr.String())
	}
}

// Negative test: a genuinely v1-shaped legacy anchor (no schema_version
// field at all) must still verify. This is the only case that legitimately
// falls through to the v1 composition path.
func TestReceiptVerifyAcceptsLegacyV1Anchor(t *testing.T) {
	tmp := t.TempDir()
	end := time.Date(2026, 4, 27, 1, 0, 0, 0, time.UTC)
	prev := strings.Repeat("0", 64)
	merkle := strings.Repeat("a", 64)
	// Compute v1 hash: sha256(prev || merkle || end).
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write([]byte(merkle))
	h.Write([]byte(end.Format(time.RFC3339Nano)))
	v1Hash := hex.EncodeToString(h.Sum(nil))

	// Build raw JSON without schema_version field at all.
	raw := fmt.Sprintf(`{
		"seq": 0,
		"interval_start": "%s",
		"interval_end": "%s",
		"node_count": 1,
		"merkle_root": "%s",
		"prev_hash": "%s",
		"hash": "%s"
	}`,
		end.Add(-time.Hour).Format(time.RFC3339Nano),
		end.Format(time.RFC3339Nano),
		merkle, prev, v1Hash,
	)
	path := filepath.Join(tmp, "legacy.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runReceiptVerify([]string{path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("legacy v1 anchor rejected: exit=%d\nstderr=%s", code, stderr.String())
	}
}

// schemaVersionPresence direct unit tests cover the JSON pre-pass.
func TestSchemaVersionPresence(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantPresent bool
		wantNull    bool
	}{
		{"absent", `{"hash":"x"}`, false, false},
		{"null", `{"schema_version":null,"hash":"x"}`, true, true},
		{"zero", `{"schema_version":0,"hash":"x"}`, true, false},
		{"two", `{"schema_version":2,"hash":"x"}`, true, false},
		{"null-with-whitespace", `{"schema_version":  null  ,"hash":"x"}`, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			present, isNull, err := schemaVersionPresence([]byte(tc.raw))
			if err != nil {
				t.Fatalf("schemaVersionPresence: %v", err)
			}
			if present != tc.wantPresent {
				t.Errorf("present = %v, want %v", present, tc.wantPresent)
			}
			if isNull != tc.wantNull {
				t.Errorf("isNull = %v, want %v", isNull, tc.wantNull)
			}
		})
	}
}

func TestReceiptRecordListExport(t *testing.T) {
	repo := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runReceiptCmd([]string{
		"record", "--repo", repo, "--task", "task-1", "--summary", "implemented feature", "--body", "diff body", "--signing-key", "secret",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("record exit=%d stderr=%s", code, stderr.String())
	}
	line := strings.TrimSpace(stdout.String())
	parts := strings.Split(line, " ")
	if len(parts) < 1 {
		t.Fatalf("stdout=%q", line)
	}
	receiptID := parts[0]
	stdout.Reset()
	stderr.Reset()
	if code := runReceiptCmd([]string{"list", "--repo", repo, "--task", "task-1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), receiptID) {
		t.Fatalf("list missing receipt id: %s", stdout.String())
	}
	outPath := filepath.Join(t.TempDir(), "receipt.json")
	stdout.Reset()
	stderr.Reset()
	if code := runReceiptCmd([]string{"export", "--repo", repo, "--id", receiptID, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("export exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("exported file missing: %v", err)
	}
}
