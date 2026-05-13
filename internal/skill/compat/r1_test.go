package compat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/skill"
)

func TestAdaptR1_RoundTrip(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{
		SchemaVersion: "2.0.0",
		Name:          "alpha",
		Version:       "0.1.0",
		Compat:        []string{"r1"},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	out, err := AdaptR1(m)
	if err != nil {
		t.Fatalf("AdaptR1: %v", err)
	}
	var desc R1Descriptor
	if err := json.Unmarshal(out, &desc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if desc.Runtime != "r1" || desc.Name != "alpha" {
		t.Fatalf("R1Descriptor = %+v", desc)
	}
}

func TestAdaptR1_RefusesNoCompat(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{Name: "x", Version: "1", Compat: []string{"cloudswarm"}}
	if _, err := AdaptR1(m); err == nil {
		t.Fatalf("want err, got nil")
	}
}

func TestAdaptR1_RejectsUnknownAssertion(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{
		Name: "x", Version: "1", Compat: []string{"r1"},
		RuntimeAssertions: map[string][]string{"r1": {"unknown_token"}},
	}
	if _, err := AdaptR1(m); err == nil || !strings.Contains(err.Error(), "unknown token") {
		t.Fatalf("want unknown token err, got %v", err)
	}
}

func TestAdapt_UnsupportedRuntime(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{Name: "x", Version: "1", Compat: []string{"r1"}}
	if _, err := Adapt("mars", m); err == nil || !strings.Contains(err.Error(), "unsupported adoption target") {
		t.Fatalf("want unsupported err, got %v", err)
	}
}

func TestAdapt_NilManifest(t *testing.T) {
	t.Parallel()
	if _, err := Adapt("r1", nil); err == nil {
		t.Fatalf("want err, got nil")
	}
}

func TestRuntimes_ContainsAll(t *testing.T) {
	t.Parallel()
	rts := Runtimes()
	want := map[string]bool{"r1": false, "cloudswarm": false, "heroa": false, "veritize": false}
	for _, r := range rts {
		want[r] = true
	}
	for k, v := range want {
		if !v {
			t.Fatalf("missing runtime %q in %v", k, rts)
		}
	}
}
