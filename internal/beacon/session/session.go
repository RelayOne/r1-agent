package session

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

type EphemeralKeypair struct {
	PublicKey  []byte
	PrivateKey []byte
}

type SessionKeys struct {
	OutgoingKey []byte
	IncomingKey []byte
	SessionID   string
}

type Frame struct {
	Counter    uint64 `json:"counter"`
	Ciphertext []byte `json:"ciphertext"`
}

type SecureChannel struct {
	aeadOut      cipher.AEAD
	aeadIn       cipher.AEAD
	nextOut      uint64
	highestIn    uint64
	seenIncoming map[uint64]struct{}
	mu           sync.Mutex
}

func NewEphemeralKeypair() (*EphemeralKeypair, error) {
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		return nil, fmt.Errorf("session: generate private key: %w", err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("session: derive public key: %w", err)
	}
	return &EphemeralKeypair{PublicKey: pub, PrivateKey: priv}, nil
}

func DeriveSessionKeys(ownPriv, peerPub []byte, role string) (*SessionKeys, error) {
	if len(ownPriv) != 32 || len(peerPub) != 32 {
		return nil, errors.New("session: x25519 keys must be 32 bytes")
	}
	if role != "device" && role != "beacon" {
		return nil, fmt.Errorf("session: unknown role %q", role)
	}
	shared, err := curve25519.X25519(ownPriv, peerPub)
	if err != nil {
		return nil, fmt.Errorf("session: x25519 derive: %w", err)
	}
	outgoing := hkdfExpand(shared, []byte("r1-beacon-outgoing"))
	incoming := hkdfExpand(shared, []byte("r1-beacon-incoming"))
	if role == "beacon" {
		outgoing, incoming = incoming, outgoing
	}
	sessionID := hex.EncodeToString(hkdfExpand(shared, []byte("r1-beacon-session-id"))[:8])
	return &SessionKeys{
		OutgoingKey: outgoing,
		IncomingKey: incoming,
		SessionID:   sessionID,
	}, nil
}

func NewSecureChannel(keys *SessionKeys) (*SecureChannel, error) {
	if keys == nil {
		return nil, errors.New("session: nil session keys")
	}
	aeadOut, err := chacha20poly1305.New(keys.OutgoingKey)
	if err != nil {
		return nil, fmt.Errorf("session: outgoing aead: %w", err)
	}
	aeadIn, err := chacha20poly1305.New(keys.IncomingKey)
	if err != nil {
		return nil, fmt.Errorf("session: incoming aead: %w", err)
	}
	return &SecureChannel{
		aeadOut:      aeadOut,
		aeadIn:       aeadIn,
		seenIncoming: make(map[uint64]struct{}),
	}, nil
}

func (c *SecureChannel) Encrypt(plaintext []byte) (*Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextOut++
	nonce := counterNonce(c.nextOut)
	return &Frame{
		Counter:    c.nextOut,
		Ciphertext: c.aeadOut.Seal(nil, nonce, plaintext, nonce),
	}, nil
}

func (c *SecureChannel) Decrypt(frame *Frame) ([]byte, error) {
	if frame == nil {
		return nil, errors.New("session: nil frame")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, seen := c.seenIncoming[frame.Counter]; seen {
		return nil, fmt.Errorf("session: replayed counter %d", frame.Counter)
	}
	if frame.Counter+1024 < c.highestIn {
		return nil, fmt.Errorf("session: stale counter %d", frame.Counter)
	}
	nonce := counterNonce(frame.Counter)
	plaintext, err := c.aeadIn.Open(nil, nonce, frame.Ciphertext, nonce)
	if err != nil {
		return nil, fmt.Errorf("session: decrypt frame %d: %w", frame.Counter, err)
	}
	c.seenIncoming[frame.Counter] = struct{}{}
	if frame.Counter > c.highestIn {
		c.highestIn = frame.Counter
	}
	return plaintext, nil
}

func SharedSAS(beaconPub, devicePub, challengeNonce, responseNonce []byte) string {
	mac := hmac.New(sha256.New, challengeNonce)
	mac.Write(beaconPub)
	mac.Write(devicePub)
	mac.Write(responseNonce)
	sum := mac.Sum(nil)
	value := binary.BigEndian.Uint32(sum[:4]) % 1000000
	return fmt.Sprintf("%06d", value)
}

func hkdfExpand(shared, info []byte) []byte {
	r := hkdf.New(sha256.New, shared, nil, info)
	out := make([]byte, chacha20poly1305.KeySize)
	_, _ = r.Read(out)
	return out
}

func counterNonce(counter uint64) []byte {
	nonce := make([]byte, chacha20poly1305.NonceSize)
	binary.BigEndian.PutUint64(nonce[len(nonce)-8:], counter)
	return nonce
}
