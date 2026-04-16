// Package encryption implements STOKE-021: per-agent encryption
// keys for sensitive-tier data in shared memory + delegation
// contexts. Each agent holds its own AES-256-GCM symmetric key
// (derived once per agent, persisted in the key-ring, never sent
// over the wire); tier-2+ data is encrypted with the agent's key
// before it lands in shared storage so a compromised channel
// can't expose another agent's data.
//
// Design notes:
//
//   - AES-256-GCM (not ChaCha20-Poly1305) because Go's stdlib
//     accelerates it on x86/arm and the block size fits the
//     sharedmem record granularity well.
//   - Keys live in a keyring, loaded from an OS-provided key
//     store in production. This package's default implementation
//     is a file-backed keyring (0600 permissions on a directory
//     Stoke creates); callers with a hardware key store can
//     inject an alternate Keyring implementation.
//   - Nonce construction: 12 random bytes per encryption. GCM
//     nonce collision odds at 2^-32 after ~4B messages — well
//     beyond Stoke's per-agent throughput.
//   - Not a replacement for transport-layer encryption. This
//     exists to protect data AT REST in shared memory and the
//     ledger; wire traffic still needs TLS.
package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Key is a 32-byte AES-256 key. Not exported as []byte so callers
// can't accidentally print it (Go's default formatter shows byte
// slices verbatim).
type Key struct {
	material [32]byte
}

// Bytes returns a COPY of the key material. Callers should use
// this only at the boundary (e.g. writing to the keyring); never
// log the result.
func (k Key) Bytes() [32]byte { return k.material }

// NewKey generates a fresh random key from crypto/rand.
func NewKey() (Key, error) {
	var k Key
	if _, err := io.ReadFull(rand.Reader, k.material[:]); err != nil {
		return Key{}, fmt.Errorf("encryption: generate key: %w", err)
	}
	return k, nil
}

// KeyFromBytes constructs a Key from pre-existing material (e.g.
// loaded from the keyring). Errors if b is not exactly 32 bytes.
func KeyFromBytes(b []byte) (Key, error) {
	var k Key
	if len(b) != 32 {
		return Key{}, fmt.Errorf("encryption: key must be 32 bytes, got %d", len(b))
	}
	copy(k.material[:], b)
	return k, nil
}

// Keyring maps agent IDs to their AES-256 keys. Implementations
// persist keys across restarts so an agent's cached data is
// decryptable after a process bounce.
type Keyring interface {
	// Get retrieves the key for agentID. Returns ErrKeyNotFound
	// when no key exists (callers typically call GetOrCreate to
	// handle that).
	Get(agentID string) (Key, error)

	// Put stores a key for agentID, replacing any existing key.
	Put(agentID string, k Key) error

	// GetOrCreate returns the key for agentID, generating + storing
	// a fresh one if none exists. The `created` flag tells the
	// caller whether the key is brand-new (which matters for
	// provisioning flows that need to publish the public identity
	// side).
	GetOrCreate(agentID string) (k Key, created bool, err error)

	// Delete removes the key for agentID. Idempotent.
	Delete(agentID string) error
}

// ErrKeyNotFound is returned by Get when no key exists for the
// agent.
var ErrKeyNotFound = errors.New("encryption: key not found")

// MemoryKeyring is an in-memory Keyring. Suitable for tests and
// ephemeral scratch runs; production uses FileKeyring (or an
// operator-supplied HSM-backed variant).
type MemoryKeyring struct {
	mu   sync.Mutex
	keys map[string]Key
}

// NewMemoryKeyring returns an empty in-memory keyring.
func NewMemoryKeyring() *MemoryKeyring {
	return &MemoryKeyring{keys: map[string]Key{}}
}

func (m *MemoryKeyring) Get(agentID string) (Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[agentID]
	if !ok {
		return Key{}, ErrKeyNotFound
	}
	return k, nil
}

func (m *MemoryKeyring) Put(agentID string, k Key) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[agentID] = k
	return nil
}

func (m *MemoryKeyring) GetOrCreate(agentID string) (Key, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.keys[agentID]; ok {
		return k, false, nil
	}
	k, err := NewKey()
	if err != nil {
		return Key{}, false, err
	}
	m.keys[agentID] = k
	return k, true, nil
}

func (m *MemoryKeyring) Delete(agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, agentID)
	return nil
}

// FileKeyring persists keys to a directory. Each key lives in a
// file named <agentID>.key with 0600 permissions; the directory
// has 0700 permissions. Loading re-reads the file on every Get
// rather than caching — keeps the keyring footprint small and
// ensures an out-of-process key rotation is picked up without a
// process bounce.
type FileKeyring struct {
	mu  sync.Mutex
	dir string
}

// NewFileKeyring returns a keyring rooted at dir, creating the
// directory with 0700 perms if it doesn't exist.
func NewFileKeyring(dir string) (*FileKeyring, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("encryption: keyring dir: %w", err)
	}
	return &FileKeyring{dir: dir}, nil
}

func (f *FileKeyring) path(agentID string) string {
	// Sanitize via base64 so slashes/special chars in agent IDs
	// don't escape the keyring directory. Keys are already opaque
	// so the extra encoding is cheap.
	enc := base64.RawURLEncoding.EncodeToString([]byte(agentID))
	return filepath.Join(f.dir, enc+".key")
}

func (f *FileKeyring) Get(agentID string) (Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := os.ReadFile(f.path(agentID))
	if os.IsNotExist(err) {
		return Key{}, ErrKeyNotFound
	}
	if err != nil {
		return Key{}, fmt.Errorf("encryption: read key: %w", err)
	}
	return KeyFromBytes(b)
}

func (f *FileKeyring) Put(agentID string, k Key) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := k.material[:]
	// Atomic write via temp + rename — a partial-write crash
	// would corrupt a critical key file otherwise.
	tmp := f.path(agentID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("encryption: write key tmp: %w", err)
	}
	if err := os.Rename(tmp, f.path(agentID)); err != nil {
		return fmt.Errorf("encryption: rename key: %w", err)
	}
	return nil
}

func (f *FileKeyring) GetOrCreate(agentID string) (Key, bool, error) {
	k, err := f.Get(agentID)
	if err == nil {
		return k, false, nil
	}
	if !errors.Is(err, ErrKeyNotFound) {
		return Key{}, false, err
	}
	newKey, err := NewKey()
	if err != nil {
		return Key{}, false, err
	}
	if err := f.Put(agentID, newKey); err != nil {
		return Key{}, false, err
	}
	return newKey, true, nil
}

func (f *FileKeyring) Delete(agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	err := os.Remove(f.path(agentID))
	if os.IsNotExist(err) {
		return nil // idempotent
	}
	return err
}

// Encrypt seals plaintext with k using AES-256-GCM. Output is
// nonce (12 bytes) || ciphertext (len(plaintext)+16 tag bytes).
// Decrypt(k, Encrypt(k, x)) == x; with a different key it errors.
func Encrypt(k Key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(k.material[:])
	if err != nil {
		return nil, fmt.Errorf("encryption: cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("encryption: gcm init: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("encryption: nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt opens ciphertext produced by Encrypt under the same
// key. Returns an error if the tag doesn't verify (wrong key,
// tampered ciphertext, truncated input).
func Decrypt(k Key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(k.material[:])
	if err != nil {
		return nil, fmt.Errorf("encryption: cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("encryption: gcm init: %w", err)
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("encryption: ciphertext too short")
	}
	nonce := ciphertext[:gcm.NonceSize()]
	ct := ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}
