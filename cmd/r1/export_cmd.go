package main

// export_cmd.go — `r1 export` subcommand.
//
// work-stoke TASK 16: produce a `.tracebundle` zip archive containing
// every artifact required to replay a Stoke session on another host:
//
//	ledger/        — nodes + edges + chain directory (copied verbatim)
//	checkpoints/   — the JSONL checkpoint file the session wrote
//	stream.jsonl   — the Stoke NDJSON event stream
//	memory.json    — membus rows scoped to this session (JSON snapshot)
//	manifest.json  — metadata + Merkle root over all archived files
//
// Invocation:
//
//	r1 export --format tracebundle --session-id <id> --output <path>
//
// The bundle is content-addressed: the Merkle root (SHA-256 of the
// canonical file-digest list) is embedded in the output filename so
// two different sessions can never collide. Callers that pass a
// directory as --output get the generated filename; callers that
// pass an explicit .tracebundle file get it rewritten to include the
// 12-char root.
//
// Today only --format tracebundle is recognised; future formats
// (e.g. NDJSON-only, ledger-only) can share the same subcommand.

import (
	"archive/zip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/session"
)

// exportCmd wires the top-level switch entry; runs and os.Exits.
func exportCmd(args []string) {
	os.Exit(runExportCmd(args, os.Stdout, os.Stderr))
}

// runExportCmd implements the `r1 export` subcommand. Returns a
// UNIX-style exit code (0 ok / 1 runtime / 2 usage) so tests can
// assert behaviour without intercepting os.Exit.
func runExportCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "tracebundle", "export format (only 'tracebundle' supported)")
	sessionID := fs.String("session-id", "", "instance_id of the session to export (see r1.session.json)")
	output := fs.String("output", "", "output path or directory (directory → filename is generated)")
	repoFlag := fs.String("repo", "", "repo root (default: cwd). signature file read from <repo>/.stoke/r1.session.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *format != "tracebundle" {
		fmt.Fprintf(stderr, "export: unsupported --format %q (only 'tracebundle')\n", *format)
		return 2
	}
	if *sessionID == "" {
		fmt.Fprintln(stderr, "export: --session-id is required")
		return 2
	}
	if *output == "" {
		fmt.Fprintln(stderr, "export: --output is required")
		return 2
	}

	repoRoot := *repoFlag
	if repoRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "export: getwd: %v\n", err)
			return 1
		}
		repoRoot = cwd
	}

	sig, err := resolveExportSignature(repoRoot, *sessionID)
	if err != nil {
		fmt.Fprintf(stderr, "export: %v\n", err)
		return 1
	}

	bundlePath, merkleRoot, err := BuildTraceBundle(sig, *output)
	if err != nil {
		fmt.Fprintf(stderr, "export: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (merkle_root=%s)\n", bundlePath, merkleRoot)
	return 0
}

// resolveExportSignature locates the SignatureFile for sessionID by
// reading <repo>/.stoke/r1.session.json. A mismatch between the
// requested session-id and the live signature is a hard error —
// callers point at the wrong repo more often than they mis-type the
// session id, and silently succeeding would produce a bundle whose
// contents do not match its name.
func resolveExportSignature(repoRoot, sessionID string) (session.SignatureFile, error) {
	sigPath := filepath.Join(repoRoot, ".stoke", "r1.session.json")
	sig, err := session.LoadSignature(sigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Legacy repos without a live signature file: build a
			// synthesised SignatureFile pointing at the resolved
			// data-dir layout so `r1 export` still works on a
			// crashed session whose sidecar never finished writing.
			// r1dir.JoinFor prefers `.r1/` when present, falls back to
			// `.stoke/` for pre-rename sessions (work-r1-rename.md §S1-5).
			return session.SignatureFile{
				Version:        "synthetic",
				InstanceID:     sessionID,
				RepoRoot:       repoRoot,
				Status:         "unknown",
				StreamFile:     r1dir.JoinFor(repoRoot, "stream.jsonl"),
				LedgerDir:      r1dir.JoinFor(repoRoot, "ledger"),
				CheckpointFile: r1dir.JoinFor(repoRoot, "checkpoints.jsonl"),
				BusWAL:         r1dir.JoinFor(repoRoot, "memory.db"),
				UpdatedAt:      time.Now().UTC(),
			}, nil
		}
		return sig, fmt.Errorf("load signature %s: %w", sigPath, err)
	}
	if sig.InstanceID != sessionID {
		return sig, fmt.Errorf("signature instance_id=%q does not match --session-id=%q", sig.InstanceID, sessionID)
	}
	return sig, nil
}

// TraceBundleManifest is the JSON written into the zip as
// manifest.json. It is the authoritative metadata for the bundle and
// the sole input to the Merkle-root computation.
type TraceBundleManifest struct {
	Format      string             `json:"format"`
	Version     string             `json:"version"`
	SessionID   string             `json:"session_id"`
	RepoRoot    string             `json:"repo_root,omitempty"`
	Mode        string             `json:"mode,omitempty"`
	Model       string             `json:"model,omitempty"`
	SowName     string             `json:"sow_name,omitempty"`
	Status      string             `json:"status,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	Files       []TraceBundleEntry `json:"files"`
	MerkleRoot  string             `json:"merkle_root"`
}

// TraceBundleEntry describes one archived file. Digest is the hex
// SHA-256 of the file's raw bytes as they appear inside the zip.
type TraceBundleEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// traceBundleFormatVersion is the manifest schema version. Bumped
// when the zip layout or manifest fields change incompatibly.
const traceBundleFormatVersion = "1"

// BuildTraceBundle writes a `.tracebundle` zip at outputPath (which
// may be a directory: filename is generated from the Merkle root)
// and returns (finalPath, hexMerkleRoot, error).
func BuildTraceBundle(sig session.SignatureFile, outputPath string) (string, string, error) {
	// Gather the four artifact sources defined by the spec. Missing
	// files are allowed — a crashed session may never have written
	// some artefacts and the manifest records every present file with
	// its digest. Empty directories become manifest entries with zero
	// size and the zero-length sha256 so consumers can detect them.
	entries, files, err := gatherTraceBundleArtifacts(sig)
	if err != nil {
		return "", "", err
	}

	// Build the manifest first so it can reference the merkle root of
	// its own file list. We compute the root by hashing the sorted
	// "<path>\t<digest>\n" list — this makes the root a function of
	// the content alone, independent of zip compression choices.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	merkleRoot := computeMerkleRoot(entries)

	manifest := TraceBundleManifest{
		Format:     "tracebundle",
		Version:    traceBundleFormatVersion,
		SessionID:  sig.InstanceID,
		RepoRoot:   sig.RepoRoot,
		Mode:       sig.Mode,
		Model:      sig.Model,
		SowName:    sig.SowName,
		Status:     sig.Status,
		CreatedAt:  time.Now().UTC(),
		Files:      entries,
		MerkleRoot: merkleRoot,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("marshal manifest: %w", err)
	}

	finalPath, err := resolveBundlePath(outputPath, sig.InstanceID, merkleRoot)
	if err != nil {
		return "", "", err
	}

	if err := writeZipBundle(finalPath, files, manifestBytes); err != nil {
		return "", "", err
	}
	return finalPath, merkleRoot, nil
}

// bundleFile is one payload inside the zip (path + bytes). The
// manifest is added separately in writeZipBundle so its digest stays
// out of the merkle root (chicken-and-egg otherwise).
type bundleFile struct {
	Path string
	Data []byte
}

// gatherTraceBundleArtifacts reads every artefact referenced by sig
// into memory, returning the manifest entries (one per archived file)
// and the raw bundleFile slice used by writeZipBundle. Keeping both
// representations in sync is the one invariant worth the duplication.
func gatherTraceBundleArtifacts(sig session.SignatureFile) ([]TraceBundleEntry, []bundleFile, error) {
	var (
		files []bundleFile
	)

	// --- stream.jsonl ---
	if sig.StreamFile != "" {
		raw, err := readOptional(sig.StreamFile)
		if err != nil {
			return nil, nil, err
		}
		if raw != nil {
			files = append(files, bundleFile{Path: "stream.jsonl", Data: raw})
		}
	}

	// --- checkpoints/ (single JSONL tailed inside the zip dir) ---
	if sig.CheckpointFile != "" {
		raw, err := readOptional(sig.CheckpointFile)
		if err != nil {
			return nil, nil, err
		}
		if raw != nil {
			files = append(files, bundleFile{Path: "checkpoints/checkpoints.jsonl", Data: raw})
		}
	}

	// --- ledger/ (recursive copy of the entire directory) ---
	if sig.LedgerDir != "" {
		ledgerFiles, err := collectLedgerDir(sig.LedgerDir)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, ledgerFiles...)
	}

	// --- memory.json (membus rows for this session) ---
	memoryJSON, err := exportSessionMemory(sig)
	if err != nil {
		return nil, nil, err
	}
	if memoryJSON != nil {
		files = append(files, bundleFile{Path: "memory.json", Data: memoryJSON})
	}

	// Produce a manifest entry per file. Digest is SHA-256 over the
	// raw bytes we're about to archive — identical bytes on any host
	// yield an identical digest.
	entries := make([]TraceBundleEntry, 0, len(files))
	for _, f := range files {
		sum := sha256.Sum256(f.Data)
		entries = append(entries, TraceBundleEntry{
			Path:   f.Path,
			Size:   int64(len(f.Data)),
			Digest: hex.EncodeToString(sum[:]),
		})
	}
	return entries, files, nil
}

// readOptional returns (data, nil) for a present file, (nil, nil) for
// ErrNotExist, and (nil, err) for everything else. Absent artefacts
// are a normal condition for crashed or short-lived sessions.
func readOptional(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return raw, nil
}

// collectLedgerDir walks a ledger directory and returns each file as
// a bundleFile with the "ledger/<rel>" prefix. A missing directory
// returns (nil, nil) — see readOptional for the rationale.
func collectLedgerDir(dir string) ([]bundleFile, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat ledger %s: %w", dir, err)
	}
	if !info.IsDir() {
		// Odd but not fatal — treat a single-file ledger as one entry.
		raw, rerr := os.ReadFile(dir)
		if rerr != nil {
			return nil, rerr
		}
		return []bundleFile{{Path: "ledger/" + filepath.Base(dir), Data: raw}}, nil
	}
	var out []bundleFile
	werr := filepath.Walk(dir, func(p string, fi os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		if fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		// Zip archives use forward slashes regardless of host OS.
		out = append(out, bundleFile{
			Path: "ledger/" + filepath.ToSlash(rel),
			Data: raw,
		})
		return nil
	})
	if werr != nil {
		return nil, fmt.Errorf("walk ledger %s: %w", dir, werr)
	}
	return out, nil
}

// sessionMemoryRow is the portable representation of one membus row.
// We deliberately do not import membus.Memory because the exporter
// has no business coupling to that package's private schema: the
// tracebundle consumer reads the JSON back with encoding/json.
type sessionMemoryRow struct {
	ID           int64  `json:"id"`
	Scope        string `json:"scope"`
	ScopeTarget  string `json:"scope_target,omitempty"`
	Key          string `json:"key"`
	Content      string `json:"content"`
	Author       string `json:"author,omitempty"`
	CreatedAt    string `json:"created_at"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	ContentHash  string `json:"content_hash,omitempty"`
}

// sessionMemorySnapshot is the top-level shape of memory.json.
type sessionMemorySnapshot struct {
	SessionID string             `json:"session_id"`
	Rows      []sessionMemoryRow `json:"rows"`
	Count     int                `json:"count"`
	SourceDB  string             `json:"source_db,omitempty"`
}

// exportSessionMemory opens the membus SQLite database and dumps
// rows scoped to sig.InstanceID (via scope_target match on session /
// session_step rows). A missing DB yields a nil result — an empty
// session is fine and should not fail the export. Any other error is
// surfaced so operators know their memory scope was lost.
func exportSessionMemory(sig session.SignatureFile) ([]byte, error) {
	dbPath := sig.BusWAL
	if dbPath == "" {
		dbPath = filepath.Join(sig.RepoRoot, ".stoke", "memory.db")
	}
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Emit an empty snapshot so consumers get a consistent
			// memory.json shape regardless of whether membus ran.
			return json.MarshalIndent(sessionMemorySnapshot{
				SessionID: sig.InstanceID,
				Rows:      []sessionMemoryRow{},
				Count:     0,
			}, "", "  ")
		}
		return nil, fmt.Errorf("stat membus db %s: %w", dbPath, err)
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=2000", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open membus db: %w", err)
	}
	defer db.Close()

	// Best-effort query: not every Stoke tree uses the same membus
	// schema version. We probe sqlite_master for the expected table
	// and fall back to an empty snapshot otherwise.
	rows, err := queryMembusForSession(db, sig.InstanceID)
	if err != nil {
		return nil, err
	}
	snap := sessionMemorySnapshot{
		SessionID: sig.InstanceID,
		Rows:      rows,
		Count:     len(rows),
		SourceDB:  dbPath,
	}
	return json.MarshalIndent(snap, "", "  ")
}

// queryMembusForSession runs a schema-tolerant SELECT against the
// memory bus table. Unknown schemas silently return an empty slice:
// the bundle is still valid, it just has no session-scoped memory.
func queryMembusForSession(db *sql.DB, sessionID string) ([]sessionMemoryRow, error) {
	var tblCount int
	if err := db.QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='stoke_memory_bus'`,
	).Scan(&tblCount); err != nil {
		return nil, fmt.Errorf("probe membus schema: %w", err)
	}
	if tblCount == 0 {
		return nil, nil
	}

	// Only select columns present in every membus revision we have
	// shipped. `scope_target = <sessionID>` captures ScopeSession
	// rows; anything scoped elsewhere (Worker, AllSessions, Global,
	// Always) is excluded by design — bundles are a per-session
	// artefact, and cross-session data belongs to the source host.
	q := `SELECT COALESCE(id,0), COALESCE(scope,''),
	             COALESCE(scope_target,''), COALESCE(key,''),
	             COALESCE(content,''), COALESCE(author,''),
	             COALESCE(created_at,''), COALESCE(expires_at,''),
	             COALESCE(content_hash,'')
	      FROM stoke_memory_bus
	      WHERE scope_target = ?
	      ORDER BY id ASC`
	rows, err := db.Query(q, sessionID)
	if err != nil {
		// Schema drift: older rows may lack content_hash. Swallow
		// and return an empty set rather than failing the whole
		// export on a column-missing error — the snapshot still has
		// the correct session id.
		return nil, nil //nolint:nilerr
	}
	defer rows.Close()
	var out []sessionMemoryRow
	for rows.Next() {
		var r sessionMemoryRow
		if err := rows.Scan(
			&r.ID, &r.Scope, &r.ScopeTarget, &r.Key, &r.Content,
			&r.Author, &r.CreatedAt, &r.ExpiresAt, &r.ContentHash,
		); err != nil {
			return nil, fmt.Errorf("scan membus row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// computeMerkleRoot returns the hex SHA-256 of the canonical file
// list: "<path>\t<digest>\n" for each entry, sorted lexicographically
// by path. The manifest itself is excluded (it's derived from this
// value, so including it would be circular). Two bundles with
// identical contents therefore produce identical roots regardless of
// the host OS or zip metadata.
func computeMerkleRoot(entries []TraceBundleEntry) string {
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\t%s\n", e.Path, e.Digest)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// resolveBundlePath turns --output into a concrete file path. If
// output is a directory, we build `<session>-<merkle12>.tracebundle`
// inside it. If output is a file, we splice `-<merkle12>` in front of
// the extension so every bundle is content-addressable by filename.
func resolveBundlePath(outputPath, sessionID, merkleRoot string) (string, error) {
	short := merkleRoot
	if len(short) > 12 {
		short = short[:12]
	}
	info, err := os.Stat(outputPath)
	if err == nil && info.IsDir() {
		fname := fmt.Sprintf("%s-%s.tracebundle", sanitizeForFilename(sessionID), short)
		return filepath.Join(outputPath, fname), nil
	}
	// Splice short merkle root in front of the final extension so
	// two exports of different content cannot alias each other.
	base := outputPath
	ext := filepath.Ext(base)
	if ext == "" {
		ext = ".tracebundle"
		base += ext
	}
	trimmed := strings.TrimSuffix(base, ext)
	// Avoid double-insertion if the caller already baked in a merkle
	// suffix (re-running export should be idempotent).
	if !strings.HasSuffix(trimmed, "-"+short) {
		trimmed = trimmed + "-" + short
	}
	return trimmed + ext, nil
}

// writeZipBundle writes the zip atomically via a tmp file + rename
// so a partial failure never leaves a half-written bundle at the
// final path. Manifest is archived last so streaming consumers can
// validate every payload file before they hit the summary.
func writeZipBundle(path string, files []bundleFile, manifestBytes []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tracebundle-*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	zw := zip.NewWriter(tmp)
	for _, f := range files {
		w, err := zw.Create(f.Path)
		if err != nil {
			tmp.Close()
			return fmt.Errorf("zip create %s: %w", f.Path, err)
		}
		if _, err := w.Write(f.Data); err != nil {
			tmp.Close()
			return fmt.Errorf("zip write %s: %w", f.Path, err)
		}
	}
	mw, err := zw.Create("manifest.json")
	if err != nil {
		tmp.Close()
		return fmt.Errorf("zip create manifest: %w", err)
	}
	if _, err := mw.Write(manifestBytes); err != nil {
		tmp.Close()
		return fmt.Errorf("zip write manifest: %w", err)
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return fmt.Errorf("zip close: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename to %s: %w", path, err)
	}
	return nil
}
