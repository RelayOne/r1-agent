package skillmfr

import (
	"encoding/json"
	"errors"
	"testing"
)

func validManifest() Manifest {
	return Manifest{
		Name:         "code-search",
		Version:      "1.0.0",
		Description:  "Search the codebase for symbols",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"results":{"type":"array"}}}`),
		WhenToUse:    []string{"find a function by name"},
		WhenNotToUse: []string{"modify source files", "execute code"},
	}
}

func TestValidate_Ok(t *testing.T) {
	m := validManifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_MissingFieldsErrors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Manifest)
	}{
		{"no name", func(m *Manifest) { m.Name = "" }},
		{"no version", func(m *Manifest) { m.Version = "" }},
		{"no description", func(m *Manifest) { m.Description = "" }},
		{"no input schema", func(m *Manifest) { m.InputSchema = nil }},
		{"null input schema", func(m *Manifest) { m.InputSchema = json.RawMessage(`null`) }},
		{"no output schema", func(m *Manifest) { m.OutputSchema = nil }},
		{"empty whenToUse", func(m *Manifest) { m.WhenToUse = nil }},
		{"single whenNotToUse", func(m *Manifest) { m.WhenNotToUse = []string{"only one"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := validManifest()
			c.mut(&m)
			err := m.Validate()
			if !errors.Is(err, ErrIncompleteManifest) {
				t.Errorf("want ErrIncompleteManifest, got %v", err)
			}
		})
	}
}

func TestComputeHash_Deterministic(t *testing.T) {
	m := validManifest()
	h1, err := m.ComputeHash()
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	h2, _ := m.ComputeHash()
	if h1 != h2 {
		t.Errorf("hash non-deterministic: %q vs %q", h1, h2)
	}
}

func TestComputeHash_ChangesOnFieldMutation(t *testing.T) {
	m := validManifest()
	h1, _ := m.ComputeHash()
	m.Description = "totally different"
	h2, _ := m.ComputeHash()
	if h1 == h2 {
		t.Error("hash should change when Description changes")
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	m := validManifest()
	if err := r.Register(m); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get("code-search")
	if !ok {
		t.Fatal("Get returned !ok for registered manifest")
	}
	if got.Name != "code-search" {
		t.Errorf("name=%q", got.Name)
	}
}

func TestRegistry_RejectsIncompleteManifest(t *testing.T) {
	r := NewRegistry()
	bad := validManifest()
	bad.WhenNotToUse = []string{"only one"}
	err := r.Register(bad)
	if !errors.Is(err, ErrIncompleteManifest) {
		t.Errorf("want ErrIncompleteManifest, got %v", err)
	}
	// Registry stays empty.
	if len(r.List()) != 0 {
		t.Error("registry should be empty after rejected register")
	}
}

func TestRegistry_List_Sorted(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"zebra", "alpha", "mango"} {
		m := validManifest()
		m.Name = n
		_ = r.Register(m)
	}
	got := r.List()
	if len(got) != 3 {
		t.Fatalf("len=%d want 3", len(got))
	}
	want := []string{"alpha", "mango", "zebra"}
	for i, m := range got {
		if m.Name != want[i] {
			t.Errorf("[%d]=%q want %q", i, m.Name, want[i])
		}
	}
}

func TestRecordInvoke_ReturnsHash(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(validManifest())
	h, err := r.RecordInvoke("code-search")
	if err != nil {
		t.Fatalf("RecordInvoke: %v", err)
	}
	if h == "" {
		t.Error("hash should be populated")
	}
	// Hash should match ComputeHash on the same manifest.
	m, _ := r.Get("code-search")
	wanted, _ := m.ComputeHash()
	if h != wanted {
		t.Errorf("RecordInvoke hash %q != ComputeHash %q", h, wanted)
	}
}

func TestRecordInvoke_UnknownErrors(t *testing.T) {
	r := NewRegistry()
	_, err := r.RecordInvoke("missing")
	if !errors.Is(err, ErrManifestNotFound) {
		t.Errorf("want ErrManifestNotFound, got %v", err)
	}
}

func TestScaffoldFromOpenAPI_FailsValidation(t *testing.T) {
	// The scaffold helper intentionally returns an un-ready
	// skeleton — callers must fill in whenToUse/whenNotToUse
	// before Register. So the scaffold itself must FAIL
	// Validate() (otherwise an operator might register the
	// skeleton as-is).
	m := ScaffoldFromOpenAPI("foo", "1", "GET /foo",
		json.RawMessage(`{"type":"object"}`),
		json.RawMessage(`{"type":"object"}`))
	if err := m.Validate(); err == nil {
		t.Error("scaffold should fail Validate so it can't be registered without review")
	}
}

func TestValidate_UseIRRequiresRefs(t *testing.T) {
	m := validManifest()
	m.UseIR = true
	if err := m.Validate(); !errors.Is(err, ErrIncompleteManifest) {
		t.Fatalf("want ErrIncompleteManifest, got %v", err)
	}

	m.IRRef = "skills/demo/skill.r1.json"
	if err := m.Validate(); !errors.Is(err, ErrIncompleteManifest) {
		t.Fatalf("want ErrIncompleteManifest for missing compileProofRef, got %v", err)
	}

	m.CompileProofRef = "skills/demo/skill.r1.proof.json"
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate with IR refs: %v", err)
	}
}
