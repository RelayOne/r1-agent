package retention

// enforce.go ships the two retention-policy enforcement entry points called
// out in specs/retention-policies.md §6:
//
//   - EnforceOnSessionEnd — run at session close. Wipes ephemeral memory-bus
//     rows owned by the ending session per §5.1 and leaves session-memory TTL
//     stamping to the sweep path.
//   - EnforceSweep        — run on an hourly ticker by r1-server. Deletes
//     expired memory-bus rows (TTL), stream files, and checkpoint files that
//     have aged past the configured duration.
//
// Both functions are best-effort: per-step errors are captured and logged via
// the returned error aggregate but never abort the enforcement pass. A failed
// DELETE should not corrupt the session close path; a missing .stoke/streams
// directory should not crash the sweep on a fresh repo.
//
// The spec's full matrix (ledger-content redaction, stream-rotation marking,
// forced-wipe) lands in later commits on this same file — this slice ships
// the two functions the task brief explicitly calls out and wires in the
// minimum glue so they pass `go build ./... && go test ./internal/retention/...`.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/memory/membus"
)

// SweepResult tallies what a single EnforceSweep pass removed. All four
// counters report per-surface deletions and are populated even on partial
// failure so operators can reason about the blast radius from the returned
// number alone.
type SweepResult struct {
	// EphemeralDeleted is always zero for EnforceSweep (ephemeral rows are
	// deleted in EnforceOnSessionEnd, not the sweep). The field lives on
	// SweepResult anyway so the r1-server sweep log line can print a single
	// struct without conditional fields.
	EphemeralDeleted int64
	// SessionExpired counts rows removed from stoke_memory_bus whose
	// expires_at column indicated they had aged past their TTL.
	SessionExpired int64
	// StreamFilesRemoved counts files deleted from .stoke/streams/ whose
	// mtime was older than the configured stream_files retention.
	StreamFilesRemoved int64
	// CheckpointsRemoved counts files deleted from .stoke/checkpoints/
	// whose mtime was older than the configured checkpoint_files retention.
	CheckpointsRemoved int64
}

// streamsDir and checkpointsDir are the conventional on-disk locations for
// per-session stream files and checkpoint records, matching the layout used
// by cmd/stoke and cmd/r1-server. They are overridable in tests via the
// package-level variables below (kept as vars, not consts, so the
// enforce_test.go harness can point them at a t.TempDir without needing a
// functional-options rewrite of the two public functions).
var (
	streamsDir     = ".stoke/streams"
	checkpointsDir = ".stoke/checkpoints"
)

// ensureMemoryTypeColumnOnce guards the one-time ALTER TABLE that adds the
// `memory_type` column the §5.1 SQL filters on. The membus core schema ships
// without that column today — the retention spec introduces it — so we add
// it lazily on first enforcement. Idempotent via PRAGMA table_info probe.
var ensureMemoryTypeColumnOnce sync.Map // map[*sql.DB]*sync.Once

// ensureMemoryTypeColumn adds the `memory_type TEXT NOT NULL DEFAULT ''`
// column to stoke_memory_bus on the given db handle if it is not already
// present. Safe to call concurrently and repeatedly — the sync.Once per
// *sql.DB means the PRAGMA probe and ALTER TABLE each run exactly once per
// handle per process.
func ensureMemoryTypeColumn(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("retention: nil db")
	}
	onceIface, _ := ensureMemoryTypeColumnOnce.LoadOrStore(db, &sync.Once{})
	once, ok := onceIface.(*sync.Once)
	if !ok {
		// LoadOrStore stored a *sync.Once by construction; anything
		// else is a programming error in this file.
		return fmt.Errorf("retention: unexpected once type %T", onceIface)
	}
	var retErr error
	once.Do(func() {
		rows, err := db.QueryContext(ctx, `PRAGMA table_info(stoke_memory_bus)`)
		if err != nil {
			retErr = fmt.Errorf("retention: probe table_info: %w", err)
			return
		}
		defer rows.Close()
		has := false
		for rows.Next() {
			var (
				cid       int
				name      string
				ctype     string
				notnull   int
				dfltValue sql.NullString
				pk        int
			)
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
				retErr = fmt.Errorf("retention: scan table_info: %w", err)
				return
			}
			if name == "memory_type" {
				has = true
			}
		}
		if err := rows.Err(); err != nil {
			retErr = fmt.Errorf("retention: iterate table_info: %w", err)
			return
		}
		if has {
			return
		}
		_, err = db.ExecContext(ctx,
			`ALTER TABLE stoke_memory_bus ADD COLUMN memory_type TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			// Two concurrent processes could race the ALTER; treat the
			// "duplicate column" case as success so the rare race doesn't
			// wedge enforcement on one of them.
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				retErr = fmt.Errorf("retention: add memory_type column: %w", err)
				return
			}
		}
	})
	return retErr
}

// durationFor returns the time.Duration equivalent of a retention.Duration
// that represents a finite TTL. For WipeAfterSession and RetainForever
// (neither of which is a ticker-driven TTL) it returns (0, false); the
// caller is expected to special-case both values.
func durationFor(d Duration) (time.Duration, bool) {
	switch d {
	case Retain7Days:
		return 7 * 24 * time.Hour, true
	case Retain30Days:
		return 30 * 24 * time.Hour, true
	case Retain90Days:
		return 90 * 24 * time.Hour, true
	case WipeAfterSession, RetainForever:
		// Not ticker-driven — caller special-cases both.
		return 0, false
	}
	return 0, false
}

// EnforceOnSessionEnd applies the session-close enforcement pass for the
// given session_id per specs/retention-policies.md §6. Today it runs the
// ephemeral-memory wipe from §5.1; the full five-step sequence (session-TTL
// stamp, ledger-content redaction, stream rotation marker) lands in follow-
// up commits — this slice is the foundational DELETE.
//
// Best-effort contract: every error is logged and aggregated into a single
// returned error so the caller in cmd/stoke/sow_native.go can decide whether
// to surface it. Retention errors MUST NOT fail the session-close path.
func EnforceOnSessionEnd(ctx context.Context, policy Policy, sessionID string, bus *membus.Bus) error {
	if bus == nil {
		return nil
	}
	if sessionID == "" {
		return fmt.Errorf("retention: EnforceOnSessionEnd: empty sessionID")
	}
	// Only run the ephemeral wipe when policy actually wants one. The spec
	// defaults to WipeAfterSession, but an operator override of
	// RetainForever must be a full no-op.
	if policy.EphemeralMemories != WipeAfterSession {
		return nil
	}
	db := bus.DB()
	if db == nil {
		return fmt.Errorf("retention: EnforceOnSessionEnd: nil db")
	}
	if err := ensureMemoryTypeColumn(ctx, db); err != nil {
		log.Printf("retention: ensure memory_type column: %v", err)
		return err
	}

	const wipeSQL = `DELETE FROM stoke_memory_bus WHERE scope IN ('session','session_step','worker') AND memory_type = 'ephemeral' AND session_id = ?`
	res, err := db.ExecContext(ctx, wipeSQL, sessionID)
	if err != nil {
		log.Printf("retention: session-end ephemeral wipe for %q: %v", sessionID, err)
		return fmt.Errorf("retention: session-end ephemeral wipe: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		log.Printf("retention: session-end wiped %d ephemeral memories for session %s", n, sessionID)
	}
	return nil
}

// EnforceSweep is the hourly ticker-driven sweep described in §6. It runs
// TTL-based expiry across three surfaces: memory-bus rows with a populated
// expires_at in the past, stream files aged past policy.StreamFiles, and
// checkpoint files aged past policy.CheckpointFiles. Each surface is
// attempted independently — a failure on one does not short-circuit the
// others — and the per-surface counters on SweepResult always reflect what
// actually got removed.
func EnforceSweep(ctx context.Context, policy Policy, bus *membus.Bus) (SweepResult, error) {
	var (
		result SweepResult
		errs   []error
	)

	// --- memory-bus TTL sweep -------------------------------------------
	if bus != nil {
		db := bus.DB()
		if db != nil {
			if err := ensureMemoryTypeColumn(ctx, db); err != nil {
				errs = append(errs, err)
			} else {
				now := time.Now().UTC().Format(time.RFC3339Nano)
				const ttlSQL = `DELETE FROM stoke_memory_bus WHERE expires_at IS NOT NULL AND expires_at < ?`
				res, err := db.ExecContext(ctx, ttlSQL, now)
				if err != nil {
					log.Printf("retention: sweep ttl delete: %v", err)
					errs = append(errs, fmt.Errorf("retention: ttl sweep: %w", err))
				} else if n, rerr := res.RowsAffected(); rerr == nil {
					result.SessionExpired = n
				}
			}
		}
	}

	// --- stream files ---------------------------------------------------
	if streamTTL, ok := durationFor(policy.StreamFiles); ok {
		n, err := sweepFilesOlderThan(streamsDir, streamTTL)
		if err != nil {
			log.Printf("retention: sweep stream files: %v", err)
			errs = append(errs, fmt.Errorf("retention: stream sweep: %w", err))
		}
		result.StreamFilesRemoved = n
	}

	// --- checkpoint files -----------------------------------------------
	if ckptTTL, ok := durationFor(policy.CheckpointFiles); ok {
		n, err := sweepFilesOlderThan(checkpointsDir, ckptTTL)
		if err != nil {
			log.Printf("retention: sweep checkpoint files: %v", err)
			errs = append(errs, fmt.Errorf("retention: checkpoint sweep: %w", err))
		}
		result.CheckpointsRemoved = n
	}

	if len(errs) > 0 {
		return result, errors.Join(errs...)
	}
	return result, nil
}

// sweepFilesOlderThan walks dir and unlinks every regular file whose mtime
// is older than now - maxAge. Missing dirs return (0, nil) per the task
// brief's "Skip if dirs don't exist" rule. Per-file unlink failures are
// aggregated into the returned error but do not halt the walk — one
// unremovable file must not block the rest of the sweep.
func sweepFilesOlderThan(dir string, maxAge time.Duration) (int64, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("retention: stat %q: %w", dir, err)
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("retention: %q is not a directory", dir)
	}

	cutoff := time.Now().Add(-maxAge)
	var (
		count    int64
		walkErrs []error
	)
	werr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Per-entry errors (e.g., a race with another deleter) should
			// not halt the whole walk.
			walkErrs = append(walkErrs, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		fi, ferr := d.Info()
		if ferr != nil {
			walkErrs = append(walkErrs, ferr)
			return nil
		}
		if fi.ModTime().After(cutoff) {
			return nil
		}
		if rerr := os.Remove(path); rerr != nil {
			walkErrs = append(walkErrs, rerr)
			return nil
		}
		count++
		return nil
	})
	if werr != nil {
		walkErrs = append(walkErrs, werr)
	}
	if len(walkErrs) > 0 {
		return count, errors.Join(walkErrs...)
	}
	return count, nil
}
