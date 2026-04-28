package main

// receipt_cmd.go — `stoke receipt` subcommand tree.
//
// Two read-only verbs in this commit:
//
//   stoke receipt verify <path-to-receipt.json | path-to-anchors-dir | .>   [--ledger DIR]
//   stoke receipt inspect <path-to-receipt.json | path-to-anchors-dir | .>  [--ledger DIR] [--json]
//
// "Receipts" in R1 are the content-addressed Merkle anchors emitted by
// the ledger's AnchorStore. Each anchor commits to a window of ledger
// nodes via `MerkleRoot` and chains to its predecessor via `PrevHash`,
// so any tamper with an interval's nodes — or any reorder/drop of an
// anchor — breaks the chain.
//
// `verify` walks the chain in sequence order and reports the first
// broken row. Exit codes: 0 clean, 1 chain broken, 2 IO / usage.
//
// `inspect` prints a structured per-anchor view of one receipt or a
// summary of an anchor directory. The receipt-level view shows the
// hash composition so an offline reviewer can recompute it manually.
//
// This subcommand exists to back the marketing-page claim
// `r1 receipt verify path/to/receipt.json` (sites/r1/spec/index.html:65)
// and to disambiguate the existing `stoke inspect` (which is the
// codebase hygiene scanner per cmd/stoke/inspect.go:14-19).
//
// Address: r1-v1-audit-domain-10 P0 #1 + #2.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/r1dir"
)

// receiptCmd dispatches to the `stoke receipt <verb>` subcommands.
// Invoked from main.go's top-level switch.
func receiptCmd(args []string) {
	os.Exit(runReceiptCmd(args, os.Stdout, os.Stderr))
}

// runReceiptCmd returns an exit code rather than calling os.Exit so
// it can be exercised by tests.
func runReceiptCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: stoke receipt <verb> [flags] [path]")
		fmt.Fprintln(stderr, "verbs: verify, inspect")
		return 2
	}
	switch args[0] {
	case "verify":
		return runReceiptVerify(args[1:], stdout, stderr)
	case "inspect":
		return runReceiptInspect(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "usage: stoke receipt <verb> [flags] [path]")
		fmt.Fprintln(stdout, "verbs:")
		fmt.Fprintln(stdout, "  verify    walk the receipt's anchor chain; exit 1 if broken")
		fmt.Fprintln(stdout, "  inspect   print structured per-anchor view of a receipt")
		return 0
	default:
		fmt.Fprintf(stderr, "receipt: unknown verb %q\n", args[0])
		return 2
	}
}

// receiptTarget resolves the user-supplied path argument into either:
//   - a single anchor JSON file (one receipt), or
//   - an anchors directory containing index.jsonl (the chain).
//
// When the input is a directory we look for `<dir>/anchors/index.jsonl`
// first (the on-disk shape AnchorStore writes), then fall back to
// `<dir>/index.jsonl` for callers passing the anchors/ subdir directly.
// When `path == "."` (or empty) and `--ledger` is unset, we resolve
// against `r1dir.JoinFor(<cwd>, "ledger")`.
type receiptTarget struct {
	// kind is one of "file" (single-receipt JSON) or "chain" (anchor dir).
	kind string
	// filePath set when kind == "file".
	filePath string
	// chainDir is the rootDir AnchorStore expects (so that
	// rootDir/anchors/index.jsonl is the chain file).
	chainDir string
}

func resolveReceiptTarget(rawPath, ledgerOverride string) (receiptTarget, error) {
	candidate := rawPath
	if candidate == "" {
		candidate = "."
	}
	if ledgerOverride != "" {
		candidate = ledgerOverride
	}

	// "." or unset → try ledger subdir under cwd.
	if candidate == "." {
		cwd, err := os.Getwd()
		if err != nil {
			return receiptTarget{}, fmt.Errorf("getwd: %w", err)
		}
		candidate = r1dir.JoinFor(cwd, "ledger")
	}

	st, err := os.Stat(candidate)
	if err != nil {
		return receiptTarget{}, fmt.Errorf("stat %s: %w", candidate, err)
	}
	if !st.IsDir() {
		return receiptTarget{kind: "file", filePath: candidate}, nil
	}

	// Directory case: find an index.jsonl.
	if _, err := os.Stat(filepath.Join(candidate, "anchors", "index.jsonl")); err == nil {
		return receiptTarget{kind: "chain", chainDir: candidate}, nil
	}
	if _, err := os.Stat(filepath.Join(candidate, "index.jsonl")); err == nil {
		// Caller passed the anchors/ subdir directly; AnchorStore
		// roots at parent.
		return receiptTarget{kind: "chain", chainDir: filepath.Dir(candidate)}, nil
	}
	return receiptTarget{}, fmt.Errorf("no anchor chain found under %s (looked for anchors/index.jsonl and index.jsonl)", candidate)
}

// runReceiptVerify implements `stoke receipt verify`. Exit codes:
//
//	0 — chain clean / single anchor passes structural checks.
//	1 — chain broken (prints first violation).
//	2 — IO error or usage error.
func runReceiptVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("receipt verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ledgerDir := fs.String("ledger", "", "explicit ledger root (default: <cwd>/.r1/ledger or .stoke/ledger)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	rawPath := ""
	if len(rest) > 0 {
		rawPath = rest[0]
	}

	target, err := resolveReceiptTarget(rawPath, *ledgerDir)
	if err != nil {
		fmt.Fprintf(stderr, "receipt verify: %v\n", err)
		return 2
	}

	switch target.kind {
	case "file":
		return verifySingleAnchor(target.filePath, stdout, stderr)
	case "chain":
		return verifyAnchorChain(target.chainDir, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "receipt verify: internal error, unknown target kind %q\n", target.kind)
		return 2
	}
}

// verifySingleAnchor verifies a one-receipt JSON file against its own
// internal hash composition. It does NOT verify the chain (the file
// has no predecessor). For chain verification, point at the anchor
// directory.
//
// Schema-version handling mirrors the canonical
// internal/ledger.verifyAnchorHash:
//
//   - explicit "schema_version": null → tampered (rejected)
//   - explicit "schema_version":   0  → tampered (rejected; field present but invalid)
//   - explicit "schema_version":   N>0 → recomputed with the v=N composition
//   - field genuinely absent          → legacy v1 (recomputed with v1 composition)
//
// Detection of the three "looks like 0" cases requires a pre-pass over
// the raw JSON because Go's json.Unmarshal collapses null/0/missing
// into the same int zero value. Without this pre-pass a tampered
// anchor whose schema_version was numerically downgraded to 0 (or
// nulled) would be silently accepted as legacy v1 — a schema-version
// downgrade attack.
func verifySingleAnchor(path string, stdout, stderr io.Writer) int {
	data, err := os.ReadFile(path) // #nosec G304 -- CLI input.
	if err != nil {
		fmt.Fprintf(stderr, "receipt verify: read %s: %v\n", path, err)
		return 2
	}
	var anchor ledger.Anchor
	if err := json.Unmarshal(data, &anchor); err != nil {
		fmt.Fprintf(stderr, "receipt verify: parse %s: %v\n", path, err)
		return 2
	}
	if anchor.Hash == "" || anchor.MerkleRoot == "" {
		fmt.Fprintf(stderr, "receipt verify: %s: missing hash or merkle_root\n", path)
		return 1
	}
	// Pre-pass over the raw JSON to detect tamper variants of
	// schema_version that json.Unmarshal collapses to 0.
	present, isNull, err := schemaVersionPresence(data)
	if err != nil {
		fmt.Fprintf(stderr, "receipt verify: parse schema_version: %v\n", err)
		return 2
	}
	if isNull {
		fmt.Fprintln(stderr, "receipt verify: SCHEMA-VERSION TAMPER")
		fmt.Fprintf(stderr, "  file:    %s\n", path)
		fmt.Fprintln(stderr, "  reason:  schema_version is explicit null (was numeric, now null)")
		return 1
	}
	if present && anchor.SchemaVersion == 0 {
		fmt.Fprintln(stderr, "receipt verify: SCHEMA-VERSION TAMPER")
		fmt.Fprintf(stderr, "  file:    %s\n", path)
		fmt.Fprintln(stderr, "  reason:  schema_version is explicit 0 (field present but invalid)")
		return 1
	}
	if anchor.SchemaVersion < 0 {
		fmt.Fprintln(stderr, "receipt verify: SCHEMA-VERSION INVALID")
		fmt.Fprintf(stderr, "  file:    %s\n", path)
		fmt.Fprintf(stderr, "  reason:  schema_version=%d (negative)\n", anchor.SchemaVersion)
		return 1
	}
	if anchor.SchemaVersion > 2 {
		// Forward-compat: a build that doesn't know about a future
		// schema cannot meaningfully verify it. Fail closed.
		fmt.Fprintln(stderr, "receipt verify: SCHEMA-VERSION UNKNOWN")
		fmt.Fprintf(stderr, "  file:    %s\n", path)
		fmt.Fprintf(stderr, "  reason:  schema_version=%d; this build supports v1 and v2\n", anchor.SchemaVersion)
		return 1
	}
	expected := computeAnchorHash(anchor, present)
	if expected != anchor.Hash {
		fmt.Fprintln(stderr, "receipt verify: HASH MISMATCH")
		fmt.Fprintf(stderr, "  file:        %s\n", path)
		fmt.Fprintf(stderr, "  declared:    %s\n", anchor.Hash)
		fmt.Fprintf(stderr, "  recomputed:  %s\n", expected)
		if !present {
			fmt.Fprintln(stderr, "  hint:        anchor has no schema_version; verified as v1 for legacy compat — a stripped v2 anchor would fail here")
		}
		return 1
	}
	fmt.Fprintf(stdout, "receipt verify: OK\n")
	fmt.Fprintf(stdout, "  file:         %s\n", path)
	fmt.Fprintf(stdout, "  seq:          %d\n", anchor.Seq)
	fmt.Fprintf(stdout, "  interval:     [%s, %s)\n",
		anchor.IntervalStart.UTC().Format(time.RFC3339Nano),
		anchor.IntervalEnd.UTC().Format(time.RFC3339Nano),
	)
	fmt.Fprintf(stdout, "  node_count:   %d\n", anchor.NodeCount)
	fmt.Fprintf(stdout, "  merkle_root:  %s\n", anchor.MerkleRoot)
	fmt.Fprintf(stdout, "  hash:         %s\n", anchor.Hash)
	fmt.Fprintf(stdout, "  prev_hash:    %s\n", anchor.PrevHash)
	return 0
}

// schemaVersionPresence inspects the raw JSON for the receipt and
// returns whether the schema_version field was present at all, and
// whether it was the explicit null literal. This gates the three
// downgrade-attack vectors that json.Unmarshal collapses:
//
//	missing field   → present=false, isNull=false  (legacy v1)
//	"...":null      → present=true,  isNull=true   (tampered)
//	"...":0         → present=true,  isNull=false  (tampered: detected via SchemaVersion==0)
//	"...":N>0       → present=true,  isNull=false  (modern record)
func schemaVersionPresence(raw []byte) (present, isNull bool, err error) {
	var probe struct {
		SchemaVersion json.RawMessage `json:"schema_version,omitempty"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false, false, err
	}
	if len(probe.SchemaVersion) == 0 {
		return false, false, nil
	}
	trim := bytesTrimSpace(probe.SchemaVersion)
	if string(trim) == "null" {
		return true, true, nil
	}
	return true, false, nil
}

// bytesTrimSpace is a tiny zero-alloc trim around the four whitespace
// runes JSON allows around values, so we don't pull in strings just
// for one call.
func bytesTrimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			i++
			continue
		}
		break
	}
	for j > i {
		switch b[j-1] {
		case ' ', '\t', '\r', '\n':
			j--
			continue
		}
		break
	}
	return b[i:j]
}

// verifyAnchorChain walks the entire chain via AnchorStore.VerifyChain
// and returns 0 on clean, 1 on any violation.
func verifyAnchorChain(chainDir string, stdout, stderr io.Writer) int {
	store, err := ledger.NewAnchorStore(chainDir)
	if err != nil {
		fmt.Fprintf(stderr, "receipt verify: open anchor store at %s: %v\n", chainDir, err)
		return 2
	}
	violations := store.VerifyChain()
	if len(violations) == 0 {
		// Tell the operator how many anchors we walked.
		chain, _ := store.ReadChain()
		fmt.Fprintf(stdout, "receipt verify: OK\n")
		fmt.Fprintf(stdout, "  chain_dir:    %s\n", chainDir)
		fmt.Fprintf(stdout, "  anchor_count: %d\n", len(chain))
		if len(chain) > 0 {
			fmt.Fprintf(stdout, "  head_hash:    %s\n", chain[len(chain)-1].Hash)
			fmt.Fprintf(stdout, "  head_seq:     %d\n", chain[len(chain)-1].Seq)
		}
		return 0
	}
	fmt.Fprintln(stderr, "receipt verify: CHAIN BROKEN")
	for _, v := range violations {
		fmt.Fprintf(stderr, "  - %s\n", v)
	}
	return 1
}

// computeAnchorHash recomputes the anchor's Hash field from its other
// fields, using the schema version's composition rule. This mirrors
// internal/ledger/anchor.go's verifyAnchorHash but is duplicated here
// so the receipt verifier is a stand-alone offline tool that does NOT
// depend on package-internal helpers.
//
// Schema versions:
//
//	1: sha256(PrevHash || MerkleRoot || IntervalEnd) — RFC3339Nano timestamps
//	2: sha256(PrevHash || MerkleRoot || IntervalStart || IntervalEnd)
//
// schemaVersionPresent is the load-bearing parameter: when SchemaVersion
// is 0 it is interpreted as legacy v1 ONLY when the JSON field was
// genuinely absent. A 0 with the field present (or null) must NOT
// reach this function — verifySingleAnchor rejects those upstream as
// downgrade-attack tampers. See HIGH-3 in PR #24.
func computeAnchorHash(a ledger.Anchor, schemaVersionPresent bool) string {
	end := a.IntervalEnd.UTC().Format(time.RFC3339Nano)
	start := a.IntervalStart.UTC().Format(time.RFC3339Nano)
	h := sha256.New()
	h.Write([]byte(a.PrevHash))
	h.Write([]byte(a.MerkleRoot))
	// Genuinely absent → v1 legacy. Versioned → use declared.
	version := a.SchemaVersion
	if version == 0 && !schemaVersionPresent {
		version = 1
	}
	if version >= 2 {
		h.Write([]byte(start))
	}
	h.Write([]byte(end))
	return hex.EncodeToString(h.Sum(nil))
}

// runReceiptInspect implements `stoke receipt inspect`. Prints a
// human-readable view of one or more receipts. Exit codes:
//
//	0 — printed.
//	2 — IO error or usage error.
//
// This is the receipt VIEWER (data-display) — distinct from `stoke
// inspect` which is the codebase HYGIENE SCANNER (cmd/stoke/inspect.go).
func runReceiptInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("receipt inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ledgerDir := fs.String("ledger", "", "explicit ledger root (default: <cwd>/.r1/ledger or .stoke/ledger)")
	jsonOut := fs.Bool("json", false, "emit a single JSON document on stdout")
	limit := fs.Int("limit", 50, "max anchors to print when inspecting a chain")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	rawPath := ""
	if len(rest) > 0 {
		rawPath = rest[0]
	}

	target, err := resolveReceiptTarget(rawPath, *ledgerDir)
	if err != nil {
		fmt.Fprintf(stderr, "receipt inspect: %v\n", err)
		return 2
	}

	switch target.kind {
	case "file":
		return inspectSingleAnchor(target.filePath, *jsonOut, stdout, stderr)
	case "chain":
		return inspectAnchorChain(target.chainDir, *limit, *jsonOut, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "receipt inspect: internal error, unknown target kind %q\n", target.kind)
		return 2
	}
}

// inspectSingleAnchor renders one receipt.
func inspectSingleAnchor(path string, jsonOut bool, stdout, stderr io.Writer) int {
	data, err := os.ReadFile(path) // #nosec G304 -- CLI input.
	if err != nil {
		fmt.Fprintf(stderr, "receipt inspect: read %s: %v\n", path, err)
		return 2
	}
	var anchor ledger.Anchor
	if err := json.Unmarshal(data, &anchor); err != nil {
		fmt.Fprintf(stderr, "receipt inspect: parse %s: %v\n", path, err)
		return 2
	}
	if jsonOut {
		out := struct {
			File   string        `json:"file"`
			Anchor ledger.Anchor `json:"anchor"`
		}{File: path, Anchor: anchor}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return 0
	}
	fmt.Fprintf(stdout, "stoke receipt — %s\n", path)
	fmt.Fprintf(stdout, "  schema_version: %d\n", anchor.SchemaVersion)
	fmt.Fprintf(stdout, "  seq:            %d\n", anchor.Seq)
	fmt.Fprintf(stdout, "  interval:       [%s, %s)\n",
		anchor.IntervalStart.UTC().Format(time.RFC3339Nano),
		anchor.IntervalEnd.UTC().Format(time.RFC3339Nano),
	)
	fmt.Fprintf(stdout, "  node_count:     %d\n", anchor.NodeCount)
	fmt.Fprintf(stdout, "  merkle_root:    %s\n", anchor.MerkleRoot)
	fmt.Fprintf(stdout, "  prev_hash:      %s\n", anchor.PrevHash)
	fmt.Fprintf(stdout, "  hash:           %s\n", anchor.Hash)
	present, isNull, err := schemaVersionPresence(data)
	if err != nil {
		fmt.Fprintf(stdout, "  hash_check:     SKIPPED (cannot parse schema_version: %v)\n", err)
		return 0
	}
	switch {
	case isNull:
		fmt.Fprintf(stdout, "  hash_check:     TAMPERED (schema_version is explicit null)\n")
	case present && anchor.SchemaVersion == 0:
		fmt.Fprintf(stdout, "  hash_check:     TAMPERED (schema_version is explicit 0)\n")
	default:
		recomputed := computeAnchorHash(anchor, present)
		if recomputed == anchor.Hash {
			fmt.Fprintf(stdout, "  hash_check:     OK (recomputed matches)\n")
		} else {
			fmt.Fprintf(stdout, "  hash_check:     MISMATCH (recomputed=%s)\n", recomputed)
		}
	}
	return 0
}

// inspectAnchorChain renders a list of receipts under an anchor dir.
func inspectAnchorChain(chainDir string, limit int, jsonOut bool, stdout, stderr io.Writer) int {
	store, err := ledger.NewAnchorStore(chainDir)
	if err != nil {
		fmt.Fprintf(stderr, "receipt inspect: open anchor store at %s: %v\n", chainDir, err)
		return 2
	}
	chain, err := store.ReadChain()
	if err != nil {
		fmt.Fprintf(stderr, "receipt inspect: read chain: %v\n", err)
		return 2
	}
	// Newest-first display when there are many.
	sort.SliceStable(chain, func(i, j int) bool { return chain[i].Seq > chain[j].Seq })
	shown := chain
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	if jsonOut {
		out := struct {
			ChainDir   string          `json:"chain_dir"`
			TotalCount int             `json:"total_count"`
			Shown      int             `json:"shown"`
			Anchors    []ledger.Anchor `json:"anchors"`
		}{ChainDir: chainDir, TotalCount: len(chain), Shown: len(shown), Anchors: shown}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return 0
	}
	fmt.Fprintf(stdout, "stoke receipt — chain at %s\n", chainDir)
	fmt.Fprintf(stdout, "  total_anchors: %d (showing newest %d)\n", len(chain), len(shown))
	if len(chain) == 0 {
		return 0
	}
	for _, a := range shown {
		fmt.Fprintf(stdout, "  seq=%-4d  nodes=%-4d  interval_end=%s  hash=%s\n",
			a.Seq,
			a.NodeCount,
			a.IntervalEnd.UTC().Format(time.RFC3339),
			shortHash(a.Hash),
		)
	}
	return 0
}

// shortHash trims a hex hash for human display.
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// errReceiptUnsupported is reserved for future use (typed errors when
// the package needs them outside CLI exit codes).
var errReceiptUnsupported = errors.New("receipt: unsupported")
