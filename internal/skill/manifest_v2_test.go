package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePackV1Only(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name+".skill"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	packYAML := "name: " + name + "\nversion: 0.2.0\ndescription: V1 only fixture\n"
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(packYAML), 0o644); err != nil {
		t.Fatalf("WriteFile pack.yaml: %v", err)
	}
	manifest := `{
"name": "` + name + `.skill",
"version": "0.2.0",
"description": "Fixture",
"inputSchema": {"type":"object"},
"outputSchema": {"type":"object"},
"whenToUse": ["fixture"],
"whenNotToUse": ["not fixture", "not me"],
"behaviorFlags": {"mutatesState": false, "requiresNetwork": false}
}`
	if err := os.WriteFile(filepath.Join(dir, name+".skill", "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}
}

func writePackV2(t *testing.T, dir string, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ManifestV2File), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile manifest.v2.json: %v", err)
	}
}

func TestLoadManifestV2_HappyMinimal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePackV1Only(t, dir, "alpha")
	writePackV2(t, dir, `{"manifest_schema_version":"2.0.0","name":"alpha","version":"0.2.0","compat":["r1"],"signature_authority":"r1"}`)

	m, err := LoadManifestV2(dir)
	if err != nil {
		t.Fatalf("LoadManifestV2: %v", err)
	}
	if m.Source != "file" {
		t.Fatalf("Source = %q, want file", m.Source)
	}
	if len(m.Compat) != 1 || m.Compat[0] != "r1" {
		t.Fatalf("Compat = %v", m.Compat)
	}
	if m.SignatureAuthority != AuthorityR1 {
		t.Fatalf("SignatureAuthority = %q", m.SignatureAuthority)
	}
}

func TestLoadManifestV2_HappyAllFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePackV1Only(t, dir, "omni")
	body := `{
"manifest_schema_version":"2.0.0",
"name":"omni",
"version":"1.0.0",
"description":"all runtimes",
"min_r1_version":"0.9",
"compat":["r1","cloudswarm","heroa","veritize"],
"runtime_assertions":{"cloudswarm":["trust_low"],"heroa":["region_us"]},
"consumer_hooks":{
  "before": {"kind":"pre_invoke","payload_schema":{"x":1}},
  "after":  {"kind":"post_invoke","payload_schema":{"x":1},"optional":true},
  "argT":   {"kind":"transform_args","payload_schema":{"x":1}},
  "retT":   {"kind":"transform_return","payload_schema":{"x":1}},
  "errM":   {"kind":"error_map","payload_schema":{"x":1}}
},
"dependencies":["base-pack"],
"signature_authority":"tenant"
}`
	writePackV2(t, dir, body)

	m, err := LoadManifestV2(dir)
	if err != nil {
		t.Fatalf("LoadManifestV2: %v", err)
	}
	if len(m.Compat) != 4 {
		t.Fatalf("Compat = %v, want 4", m.Compat)
	}
	if len(m.ConsumerHooks) != 5 {
		t.Fatalf("ConsumerHooks count = %d, want 5", len(m.ConsumerHooks))
	}
	if got := m.AssertionsFor("cloudswarm"); len(got) != 1 || got[0] != "trust_low" {
		t.Fatalf("AssertionsFor(cloudswarm) = %v", got)
	}
}

func TestLoadManifestV2_ErrorEmptyCompat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePackV1Only(t, dir, "empty")
	writePackV2(t, dir, `{"manifest_schema_version":"2.0.0","name":"empty","version":"0.1.0","compat":[]}`)
	_, err := LoadManifestV2(dir)
	if err == nil || !strings.Contains(err.Error(), "compat must list >=1") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadManifestV2_ErrorUnknownRuntime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePackV1Only(t, dir, "bad")
	writePackV2(t, dir, `{"manifest_schema_version":"2.0.0","name":"bad","version":"0.1.0","compat":["mars"]}`)
	_, err := LoadManifestV2(dir)
	if err == nil || !strings.Contains(err.Error(), "unknown runtime") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadManifestV2_ErrorUnknownAuthority(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePackV1Only(t, dir, "badauth")
	writePackV2(t, dir, `{"manifest_schema_version":"2.0.0","name":"badauth","version":"0.1.0","compat":["r1"],"signature_authority":"npc"}`)
	_, err := LoadManifestV2(dir)
	if err == nil || !strings.Contains(err.Error(), "signature_authority") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadManifestV2_ErrorUnknownHookKind(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePackV1Only(t, dir, "badhook")
	writePackV2(t, dir, `{"manifest_schema_version":"2.0.0","name":"badhook","version":"0.1.0","compat":["r1"],"consumer_hooks":{"x":{"kind":"explode","payload_schema":{"a":1}}}}`)
	_, err := LoadManifestV2(dir)
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadManifestV2_SynthesizesFromV1(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePackV1Only(t, dir, "legacy")
	m, err := LoadManifestV2(dir)
	if err != nil {
		t.Fatalf("LoadManifestV2: %v", err)
	}
	if m.Source != "synthesized_v1" {
		t.Fatalf("Source = %q, want synthesized_v1", m.Source)
	}
	if len(m.Compat) != 1 || m.Compat[0] != "r1" {
		t.Fatalf("Compat = %v, want [r1]", m.Compat)
	}
	if m.SignatureAuthority != AuthorityR1 {
		t.Fatalf("SignatureAuthority = %q", m.SignatureAuthority)
	}
	if m.Version != "0.2.0" {
		t.Fatalf("Version = %q", m.Version)
	}
}

func TestCheckCompat(t *testing.T) {
	t.Parallel()
	m := &ManifestV2{Name: "x", Compat: []string{"r1", "cloudswarm"}}
	if err := m.CheckCompat("r1"); err != nil {
		t.Fatalf("r1 err: %v", err)
	}
	if err := m.CheckCompat("heroa"); err == nil {
		t.Fatalf("heroa: want err, got nil")
	}
	if err := m.CheckCompat(""); err == nil {
		t.Fatalf("empty: want err, got nil")
	}
	if err := m.CheckCompat("mars"); err == nil {
		t.Fatalf("mars: want err, got nil")
	}
}

func TestManifestV2_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	original := &ManifestV2{
		SchemaVersion:      "2.0.0",
		Name:               "rt",
		Version:            "1.0.0",
		Compat:             []string{"r1"},
		SignatureAuthority: AuthorityR1,
		ConsumerHooks: map[string]HookSpec{
			"h": {Kind: HookKindPreInvoke, PayloadSchema: json.RawMessage(`{"a":1}`)},
		},
	}
	if err := original.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	payload, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var rt ManifestV2
	if err := json.Unmarshal(payload, &rt); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if rt.ConsumerHooks["h"].Kind != HookKindPreInvoke {
		t.Fatalf("kind lost in round trip")
	}
}
