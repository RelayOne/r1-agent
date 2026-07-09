package substrate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/plan"
)

// fakeOffer builds an offer function that returns a fixed verdict and records
// whether it was called, so tests can assert the pre-pass short-circuits before
// any (expensive) inference dispatch and never consults the engine when disabled.
type asserter struct{ t *testing.T }

func (a asserter) True(cond bool, msg string) {
	a.t.Helper()
	if !cond {
		a.t.Fatal(msg)
	}
}

func fakeOffer(res OfferResult, ok bool, called *bool) func(Config, string) (OfferResult, bool) {
	return func(Config, string) (OfferResult, bool) {
		if called != nil {
			*called = true
		}
		return res, ok
	}
}

const sampleHandler = `pub async fn get_widget(
    axum::extract::Path(id): axum::extract::Path<uuid::Uuid>,
    axum::extract::State(state): axum::extract::State<crate::AppState>,
) -> Result<axum::Json<crate::Widget>, crate::AppError> {
    let item = crate::Widget::find_by_id(&state, id).await?.ok_or(crate::AppError::NotFound)?;
    Ok(axum::Json(item))
}
`

// writeCrate seeds a repo dir with src/lib.rs containing the pre-existing
// scaffolding the engine's handler expects (Widget/AppState/AppError exist),
// mirroring the experiment fixture. Returns the repo root.
func writeCrate(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := "// existing crate scaffolding\npub struct Widget;\npub struct AppState;\npub enum AppError { NotFound }\n"
	if err := os.WriteFile(filepath.Join(root, "src", "lib.rs"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestPrePass_CoveredTask: a covered codegen task with a confident, validated hit
// is satisfied deterministically — the handler is written to the declared file at
// 0 generation tokens and the pre-pass returns a success result (the inference
// dispatch is skipped by the caller).
func TestPrePass_CoveredTask(t *testing.T) {
	root := writeCrate(t)
	task := plan.Task{
		ID:          "S1-T1",
		Description: "add a GET-by-id handler for widget returning Widget",
		Files:       []string{"src/lib.rs"},
	}
	cfg := Config{Enabled: true, Bin: "substrate", Lib: "/libs/rust-axum"}
	verdict := OfferResult{
		Hit:             true,
		Code:            sampleHandler,
		IntentType:      "axum.get_by_id",
		ValidatedSyntax: true,
		TokensUsed:      0, // structured/0-generation path
	}
	var called bool

	res := PrePassWith(cfg, fakeOffer(verdict, true, &called), task, root)

	if res == nil {
		t.Fatal("expected the covered task to be offloaded (non-nil result), got nil (fell through to inference)")
	}
	if !res.Success || res.TaskID != "S1-T1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !called {
		t.Fatal("expected the engine offer to be consulted")
	}
	got, err := os.ReadFile(filepath.Join(root, "src", "lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "pub async fn get_widget(") {
		t.Fatalf("handler was not written into target file; got:\n%s", got)
	}
}

// TestPrePass_UncoveredTask: when the engine reports a miss the pre-pass returns
// nil (fall through to inference) and writes nothing.
func TestPrePass_UncoveredTask(t *testing.T) {
	root := writeCrate(t)
	before, _ := os.ReadFile(filepath.Join(root, "src", "lib.rs"))
	task := plan.Task{
		ID:          "S1-T2",
		Description: "implement a distributed raft consensus log compaction algorithm",
		Files:       []string{"src/lib.rs"},
	}
	cfg := Config{Enabled: true, Bin: "substrate", Lib: "/libs/rust-axum"}
	miss := OfferResult{Hit: false, IntentType: "none"}

	res := PrePassWith(cfg, fakeOffer(miss, true, nil), task, root)

	if res != nil {
		t.Fatalf("expected fall-through (nil) on a miss, got %+v", res)
	}
	after, _ := os.ReadFile(filepath.Join(root, "src", "lib.rs"))
	if string(before) != string(after) {
		t.Fatal("uncovered task must not modify the target file")
	}
}

// TestPrePass_Disabled: with R1_SOW_OFFLOAD unset the pre-pass is a no-op and
// never even consults the engine, even for a would-be hit.
func TestPrePass_Disabled(t *testing.T) {
	root := writeCrate(t)
	task := plan.Task{ID: "S1-T1", Description: "add a GET-by-id handler for widget", Files: []string{"src/lib.rs"}}
	cfg := Config{Enabled: false, Bin: "substrate", Lib: "/libs/rust-axum"}
	verdict := OfferResult{Hit: true, Code: sampleHandler, ValidatedSyntax: true}
	var called bool

	res := PrePassWith(cfg, fakeOffer(verdict, true, &called), task, root)

	if res != nil {
		t.Fatalf("disabled pre-pass must return nil, got %+v", res)
	}
	if called {
		t.Fatal("disabled pre-pass must not consult the engine")
	}
}

// TestPrePass_UnvalidatedHit: a hit whose code did NOT pass the engine's syntax
// validation is treated as not-confident and falls through to inference.
func TestPrePass_UnvalidatedHit(t *testing.T) {
	assert := asserter{t}
	root := writeCrate(t)
	task := plan.Task{ID: "S1-T1", Description: "add a GET-by-id handler for widget", Files: []string{"src/lib.rs"}}
	cfg := Config{Enabled: true, Bin: "substrate", Lib: "/libs/rust-axum"}
	verdict := OfferResult{Hit: true, Code: sampleHandler, ValidatedSyntax: false}

	res := PrePassWith(cfg, fakeOffer(verdict, true, nil), task, root)
	assert.True(res == nil, "unvalidated hit must fall through (nil result)")
	assert.True(!OfferConfident(verdict), "OfferConfident must reject an unvalidated hit")
}

// TestPrePass_MultiFileTask: tasks declaring zero or multiple target files are
// not safe to place a single handler into, so they fall through.
func TestPrePass_MultiFileTask(t *testing.T) {
	root := writeCrate(t)
	cfg := Config{Enabled: true, Bin: "substrate", Lib: "/libs/rust-axum"}
	verdict := OfferResult{Hit: true, Code: sampleHandler, ValidatedSyntax: true}

	for name, files := range map[string][]string{
		"zero":     {},
		"multiple": {"src/lib.rs", "src/routes.rs"},
		"non-rust": {"src/lib.go"},
	} {
		t.Run(name, func(t *testing.T) {
			task := plan.Task{ID: "S1-T1", Description: "add a handler", Files: files}
			if res := PrePassWith(cfg, fakeOffer(verdict, true, nil), task, root); res != nil {
				t.Fatalf("%s-file task must fall through, got %+v", name, res)
			}
		})
	}
}

// TestApplyOfferedCode_Idempotent: re-applying the same handler does not
// duplicate it (a re-run / resume must not append the function twice).
func TestApplyOfferedCode_Idempotent(t *testing.T) {
	root := writeCrate(t)
	verdict := OfferResult{Hit: true, Code: sampleHandler, ValidatedSyntax: true}

	n1, err := ApplyOfferedCode(root, "src/lib.rs", verdict)
	if err != nil || n1 == 0 {
		t.Fatalf("first apply: n=%d err=%v", n1, err)
	}
	n2, err := ApplyOfferedCode(root, "src/lib.rs", verdict)
	if err != nil {
		t.Fatalf("second apply errored: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second apply should be a no-op (0 bytes), wrote %d", n2)
	}
	got, _ := os.ReadFile(filepath.Join(root, "src", "lib.rs"))
	if c := strings.Count(string(got), "pub async fn get_widget("); c != 1 {
		t.Fatalf("expected handler exactly once, found %d", c)
	}
}

// TestApplyOfferedCode_MissingTarget: a missing target file is an error (the
// caller turns this into a fall-through, letting inference create scaffolding).
func TestApplyOfferedCode_MissingTarget(t *testing.T) {
	root := t.TempDir()
	verdict := OfferResult{Hit: true, Code: sampleHandler, ValidatedSyntax: true}
	if _, err := ApplyOfferedCode(root, "src/nope.rs", verdict); err == nil {
		t.Fatal("expected error for missing target file")
	}
}

// TestPrePass_RealBinary exercises the FULL subprocess path against the real
// `substrate offer` binary using the structured (0-token) intent so it needs no
// API key and costs nothing. Skipped unless SUBSTRATE_BIN + SUBSTRATE_LIB point
// at a working install. This is the end-to-end proof that the pre-pass drives the
// actual binary, parses its JSON, and writes the validated handler at 0
// generation tokens.
func TestPrePass_RealBinary(t *testing.T) {
	bin := os.Getenv("SUBSTRATE_BIN")
	lib := os.Getenv("SUBSTRATE_LIB")
	if bin == "" || lib == "" {
		t.Skip("set SUBSTRATE_BIN and SUBSTRATE_LIB to run the real-binary integration test")
	}
	root := writeCrate(t)
	task := plan.Task{
		ID:          "S1-T1",
		Description: "add a GET-by-id handler for widget returning Widget",
		Files:       []string{"src/lib.rs"},
	}
	cfg := Config{Enabled: true, Bin: bin, Lib: lib}

	// Drive the real binary through the structured 0-token path so the test is
	// free and key-free. (Production uses --request/NL via RunOffer.)
	offer := func(c Config, _ string) (OfferResult, bool) {
		out, err := exec.Command(c.Bin, "offer",
			"--intent-type", "axum.get_by_id",
			"--param", "resource=widget", "--param", "model=Widget",
			"--lib", c.Lib).Output()
		if err != nil {
			t.Fatalf("substrate offer failed: %v", err)
		}
		var r OfferResult
		if jErr := json.Unmarshal(out, &r); jErr != nil {
			t.Fatalf("parse offer JSON: %v\n%s", jErr, out)
		}
		return r, true
	}

	res := PrePassWith(cfg, offer, task, root)
	if res == nil || !res.Success {
		t.Fatalf("expected real-binary offload success, got %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(root, "src", "lib.rs"))
	if !strings.Contains(string(got), "pub async fn get_widget(") {
		t.Fatalf("real binary did not produce the handler; got:\n%s", got)
	}
}
