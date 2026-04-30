package pairing

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/RelayOne/r1/internal/beacon/identity"
	"github.com/RelayOne/r1/internal/beacon/session"
)

const (
	ChallengeNonceSize = 32
	ResponseNonceSize  = 32
	ChallengeWindow    = 60 * time.Second
)

type Challenge struct {
	BeaconID              string
	BeaconPublicKey       ed25519.PublicKey
	BeaconFingerprint     string
	BeaconEphemeralX25519 []byte
	Nonce                 []byte
	Words                 []string
	IssuedAt              time.Time
	ExpiresAt             time.Time
}

type Response struct {
	BeaconID                string
	DevicePublicKey         ed25519.PublicKey
	DeviceEphemeralX25519   []byte
	DeviceID                string
	DeviceLabel             string
	DeviceKind              string
	ChallengeWords          []string
	Nonce                   []byte
	OperatorMasterPublicKey ed25519.PublicKey
	IssuedAt                time.Time
}

func NewChallenge(b *identity.Beacon, beaconEphemeralX25519 []byte) (*Challenge, error) {
	if b == nil {
		return nil, errors.New("pairing: beacon required")
	}
	if len(beaconEphemeralX25519) != 32 {
		return nil, errors.New("pairing: beacon ephemeral key must be 32 bytes")
	}
	nonce := make([]byte, ChallengeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("pairing: generate challenge nonce: %w", err)
	}
	now := time.Now().UTC()
	return &Challenge{
		BeaconID:              b.BeaconID,
		BeaconPublicKey:       append(ed25519.PublicKey(nil), b.PublicKey...),
		BeaconFingerprint:     b.Fingerprint(),
		BeaconEphemeralX25519: append([]byte(nil), beaconEphemeralX25519...),
		Nonce:                 nonce,
		Words:                 nonceToWords(nonce, 3),
		IssuedAt:              now,
		ExpiresAt:             now.Add(ChallengeWindow),
	}, nil
}

func (c *Challenge) Validate(now time.Time) error {
	if c == nil {
		return errors.New("pairing: nil challenge")
	}
	if c.BeaconID == "" || len(c.BeaconPublicKey) != ed25519.PublicKeySize || len(c.BeaconEphemeralX25519) != 32 {
		return errors.New("pairing: malformed challenge")
	}
	if len(c.Nonce) != ChallengeNonceSize {
		return errors.New("pairing: bad challenge nonce size")
	}
	if now.After(c.ExpiresAt) {
		return fmt.Errorf("pairing: challenge expired at %s", c.ExpiresAt.Format(time.RFC3339))
	}
	return nil
}

func NewResponse(challenge *Challenge, dev *identity.Device, deviceEphemeralX25519 []byte, operatorMasterPublicKey ed25519.PublicKey) (*Response, error) {
	if err := challenge.Validate(time.Now().UTC()); err != nil {
		return nil, err
	}
	if dev == nil {
		return nil, errors.New("pairing: device required")
	}
	if len(deviceEphemeralX25519) != 32 {
		return nil, errors.New("pairing: device ephemeral key must be 32 bytes")
	}
	nonce := make([]byte, ResponseNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("pairing: generate response nonce: %w", err)
	}
	return &Response{
		BeaconID:                challenge.BeaconID,
		DevicePublicKey:         append(ed25519.PublicKey(nil), dev.PublicKey...),
		DeviceEphemeralX25519:   append([]byte(nil), deviceEphemeralX25519...),
		DeviceID:                dev.DeviceID,
		DeviceLabel:             dev.Label,
		DeviceKind:              dev.Kind,
		ChallengeWords:          append([]string(nil), challenge.Words...),
		Nonce:                   nonce,
		OperatorMasterPublicKey: append(ed25519.PublicKey(nil), operatorMasterPublicKey...),
		IssuedAt:                time.Now().UTC(),
	}, nil
}

func (r *Response) Validate(challenge *Challenge) error {
	if challenge == nil {
		return errors.New("pairing: challenge required")
	}
	if r.BeaconID != challenge.BeaconID {
		return errors.New("pairing: beacon id mismatch")
	}
	if len(r.DevicePublicKey) != ed25519.PublicKeySize || len(r.DeviceEphemeralX25519) != 32 {
		return errors.New("pairing: malformed response")
	}
	if len(r.Nonce) != ResponseNonceSize {
		return errors.New("pairing: bad response nonce size")
	}
	if len(r.ChallengeWords) != len(challenge.Words) {
		return errors.New("pairing: challenge word count mismatch")
	}
	for i := range r.ChallengeWords {
		if r.ChallengeWords[i] != challenge.Words[i] {
			return fmt.Errorf("pairing: challenge word %d mismatch", i)
		}
	}
	return nil
}

func VerifySAS(challenge *Challenge, response *Response) (string, error) {
	if err := response.Validate(challenge); err != nil {
		return "", err
	}
	return session.SharedSAS(challenge.BeaconEphemeralX25519, response.DeviceEphemeralX25519, challenge.Nonce, response.Nonce), nil
}
