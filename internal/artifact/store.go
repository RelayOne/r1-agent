// Package artifact implements the storage and lifecycle layer for
// Artifact ledger nodes (Parity-2 from r1-parity-and-superiority.md).
//
// The package separates four concerns:
//
//	store.go    — content-addressed binary storage at .r1/artifacts/<sha>.
//	              Crypto-shred safe: deleting a file leaves the ledger
//	              chain intact via the existing salt-blinded
//	              ContentCommitment scheme.
//	builder.go  — builder API for emitting artifacts. Wraps Ledger.AddNode
//	              and ensures content_ref / size_bytes / when are
//	              populated coherently.
//	poll.go     — supervisor-side annotation poll loop. Workers call
//	              Poll() at safe points to read new annotations and act
//	              on amend / reject without mission restart.
//	antigravity/ — wire-format compatibility with Antigravity's Artifacts
//	              primitive. Bidirectional conversion plus golden tests.
//
// The package depends only on internal/ledger and stdlib. It does not
// import internal/agentloop or internal/critic so it can be reused by
// background services (e.g. the trigger receiver) without dragging the
// worker-loop dependency tree.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Store manages content-addressed artifact bytes under a root directory
// (typically .r1/artifacts/). All operations are safe for concurrent use
// across goroutines; serialized writes prevent race conditions on the
// same content hash.
type Store struct {
	root string
	mu   sync.Mutex // serializes Put to avoid duplicate temp files
}

// ErrContentMissing is returned when Get / Stat is called for a content
// hash that does not have a corresponding file. This is expected after
// crypto-shred and is not a corruption signal: callers (e.g. the dashboard
// rendering an artifact) should treat ErrContentMissing as "redacted" and
// surface accordingly.
var ErrContentMissing = errors.New("artifact: content missing (possibly crypto-shred)")

// ErrContentTampered is returned when the bytes on disk do not hash to the
// expected content_ref. This is corruption, not redaction. Callers should
// emit a tamper event and fail loudly.
var ErrContentTampered = errors.New("artifact: content hash mismatch")

// NewStore opens or creates an artifact store under root.
//
// Convention: root is the directory specified by the ledger's storage
// config, typically derived from the same R1_LEDGER_DIR or .r1/ledger
// path. The artifact store sits beside the ledger, not inside it, so
// the ledger's git tracking is not polluted by binary blobs.
func NewStore(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("artifact: NewStore: root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("artifact: mkdir %q: %w", root, err)
	}
	return &Store{root: root}, nil
}

// Root returns the storage root directory. Useful for tests and for
// integration code that needs to bind .r1/artifacts/<sha> URLs to the
// physical path.
func (s *Store) Root() string { return s.root }

// Put writes content under sha256(content) and returns the canonical
// content_ref string ("sha256:<hex>"). Subsequent Put calls with the same
// bytes are idempotent (the second Put returns the same ref and skips
// the disk write because the file already exists).
func (s *Store) Put(content []byte) (string, error) {
	if content == nil {
		return "", errors.New("artifact: Put: content is nil")
	}
	sum := sha256.Sum256(content)
	hexSum := hex.EncodeToString(sum[:])
	ref := "sha256:" + hexSum

	target := filepath.Join(s.root, hexSum)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Idempotency: skip write if file already exists with correct hash.
	if existing, err := os.ReadFile(target); err == nil {
		existingSum := sha256.Sum256(existing)
		if hex.EncodeToString(existingSum[:]) == hexSum {
			return ref, nil
		}
		// File exists but bytes don't match the name. This is corruption;
		// fail loudly rather than silently overwrite.
		return "", fmt.Errorf("%w: %s", ErrContentTampered, target)
	}

	// Atomic write: write to temp then rename. Defends against partial
	// writes if the process dies mid-Put.
	tmp, err := os.CreateTemp(s.root, ".put-*.tmp")
	if err != nil {
		return "", fmt.Errorf("artifact: create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("artifact: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("artifact: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("artifact: close temp: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("artifact: rename %q -> %q: %w", tmpName, target, err)
	}
	return ref, nil
}

// PutFromReader streams content from r into the store. Useful for large
// recordings that should not be loaded fully in memory. Returns the
// content_ref on success.
func (s *Store) PutFromReader(r io.Reader) (string, error) {
	if r == nil {
		return "", errors.New("artifact: PutFromReader: reader is nil")
	}

	tmp, err := os.CreateTemp(s.root, ".put-stream-*.tmp")
	if err != nil {
		return "", fmt.Errorf("artifact: create temp: %w", err)
	}
	tmpName := tmp.Name()
	hasher := sha256.New()
	mw := io.MultiWriter(tmp, hasher)
	if _, err := io.Copy(mw, r); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("artifact: stream copy: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("artifact: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("artifact: close temp: %w", err)
	}

	sum := hasher.Sum(nil)
	hexSum := hex.EncodeToString(sum)
	target := filepath.Join(s.root, hexSum)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Idempotency check after stream completion (we couldn't pre-compute
	// the hash before reading).
	if _, err := os.Stat(target); err == nil {
		// File exists; verify content match opportunistically. If the
		// existing file's hash mismatches its name, surface ErrContentTampered.
		existing, rerr := os.ReadFile(target)
		if rerr == nil {
			existingSum := sha256.Sum256(existing)
			if hex.EncodeToString(existingSum[:]) == hexSum {
				os.Remove(tmpName) // discard our duplicate
				return "sha256:" + hexSum, nil
			}
			os.Remove(tmpName)
			return "", fmt.Errorf("%w: %s", ErrContentTampered, target)
		}
	}

	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("artifact: rename %q -> %q: %w", tmpName, target, err)
	}
	return "sha256:" + hexSum, nil
}

// Get returns the bytes for a given content_ref. Verifies hash on read;
// any mismatch returns ErrContentTampered. Missing file returns
// ErrContentMissing wrapped with the path.
func (s *Store) Get(ref string) ([]byte, error) {
	hexSum, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(s.root, hexSum)
	data, err := os.ReadFile(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrContentMissing, target)
		}
		return nil, fmt.Errorf("artifact: read %q: %w", target, err)
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != hexSum {
		return nil, fmt.Errorf("%w: %s", ErrContentTampered, target)
	}
	return data, nil
}

// Stat returns the size and existence status for a content_ref without
// reading the bytes. Used by the artifact builder when persisting an
// already-stored artifact's metadata to a ledger node.
func (s *Store) Stat(ref string) (size int64, exists bool, err error) {
	hexSum, err := parseRef(ref)
	if err != nil {
		return 0, false, err
	}
	target := filepath.Join(s.root, hexSum)
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("artifact: stat %q: %w", target, err)
	}
	return info.Size(), true, nil
}

// Redact crypto-shreds the content for a ref by removing the file.
// The ledger node remains; the content tier is empty. Subsequent Get
// calls return ErrContentMissing. Idempotent: redacting an already-missing
// ref is a no-op.
//
// This is the runtime expression of the salt-blinded redaction model
// already in internal/ledger/ledger.go. The chain tier's
// ContentCommitment still verifies the chain after redaction; only the
// content tier is gone.
func (s *Store) Redact(ref string) error {
	hexSum, err := parseRef(ref)
	if err != nil {
		return err
	}
	target := filepath.Join(s.root, hexSum)

	s.mu.Lock()
	defer s.mu.Unlock()

	err = os.Remove(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("artifact: redact %q: %w", target, err)
	}
	return nil
}

// parseRef extracts the hex digest from a "sha256:<hex>" ref. Returns
// the hex string. Strict on prefix and length so a corrupted ref is
// caught before any disk operation.
func parseRef(ref string) (string, error) {
	const prefix = "sha256:"
	if len(ref) != len(prefix)+64 {
		return "", fmt.Errorf("artifact: invalid ref length: %d", len(ref))
	}
	if ref[:len(prefix)] != prefix {
		return "", fmt.Errorf("artifact: invalid ref prefix: %q", ref[:7])
	}
	hexPart := ref[len(prefix):]
	for _, c := range hexPart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return "", fmt.Errorf("artifact: invalid hex character in ref")
		}
	}
	return hexPart, nil
}
