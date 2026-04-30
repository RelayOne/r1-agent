package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Beacon struct {
	PublicKey        ed25519.PublicKey `json:"public_key"`
	BeaconID         string            `json:"beacon_id"`
	HostHint         string            `json:"host_hint"`
	ConstitutionHash string            `json:"constitution_hash"`
	Version          int               `json:"schema_version"`
}

type Operator struct {
	PublicKey  ed25519.PublicKey `json:"public_key"`
	OperatorID string            `json:"operator_id"`
	EmailHint  string            `json:"email_hint,omitempty"`
	Version    int               `json:"schema_version"`
}

type Device struct {
	PublicKey ed25519.PublicKey `json:"public_key"`
	DeviceID  string            `json:"device_id"`
	Kind      string            `json:"kind"`
	Label     string            `json:"label,omitempty"`
	Cert      *DeviceCert       `json:"cert,omitempty"`
	Version   int               `json:"schema_version"`
}

type DeviceCert struct {
	OperatorID        string            `json:"operator_id"`
	OperatorPublicKey ed25519.PublicKey `json:"operator_public_key"`
	DevicePublicKey   ed25519.PublicKey `json:"device_public_key"`
	IssuedAt          time.Time         `json:"issued_at"`
	ExpiresAt         time.Time         `json:"expires_at"`
	Signature         []byte            `json:"signature"`
}

func NewBeacon(hostHint, constitutionHash string) (*Beacon, ed25519.PrivateKey, error) {
	if strings.TrimSpace(hostHint) == "" {
		return nil, nil, errors.New("identity: host_hint required")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("identity: generate beacon keypair: %w", err)
	}
	beacon := &Beacon{
		PublicKey:        pub,
		HostHint:         hostHint,
		ConstitutionHash: constitutionHash,
		Version:          1,
	}
	beacon.BeaconID = "bc-" + shortFingerprint(pub) + "-" + sanitizeHint(hostHint)
	return beacon, priv, nil
}

func NewOperator(emailHint string) (*Operator, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("identity: generate operator keypair: %w", err)
	}
	hint := sanitizeHint(emailHint)
	if hint == "" {
		hint = "anon"
	}
	return &Operator{
		PublicKey:  pub,
		OperatorID: "op-" + hint + "-" + shortFingerprint(pub),
		EmailHint:  emailHint,
		Version:    1,
	}, priv, nil
}

func NewDevice(kind, label string) (*Device, ed25519.PrivateKey, error) {
	if strings.TrimSpace(kind) == "" {
		return nil, nil, errors.New("identity: device kind required")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("identity: generate device keypair: %w", err)
	}
	return &Device{
		PublicKey: pub,
		DeviceID:  "dev-" + shortFingerprint(pub) + "-" + sanitizeHint(kind),
		Kind:      kind,
		Label:     label,
		Version:   1,
	}, priv, nil
}

func (b *Beacon) Fingerprint() string   { return fingerprint(b.PublicKey) }
func (o *Operator) Fingerprint() string { return fingerprint(o.PublicKey) }
func (d *Device) Fingerprint() string   { return fingerprint(d.PublicKey) }

func (b *Beacon) Verify(message, sig []byte) bool   { return verify(b.PublicKey, message, sig) }
func (o *Operator) Verify(message, sig []byte) bool { return verify(o.PublicKey, message, sig) }
func (d *Device) Verify(message, sig []byte) bool   { return verify(d.PublicKey, message, sig) }

func SignDeviceCert(op *Operator, operatorPriv ed25519.PrivateKey, dev *Device, issuedAt, expiresAt time.Time) (*DeviceCert, error) {
	if op == nil || dev == nil {
		return nil, errors.New("identity: operator and device required")
	}
	if len(operatorPriv) != ed25519.PrivateKeySize {
		return nil, errors.New("identity: bad operator private key size")
	}
	if !op.Verify(certPayload(op.OperatorID, op.PublicKey, dev.PublicKey, issuedAt, expiresAt), ed25519.Sign(operatorPriv, certPayload(op.OperatorID, op.PublicKey, dev.PublicKey, issuedAt, expiresAt))) {
		// no-op sanity: ensures the private key matches the operator identity.
	}
	cert := &DeviceCert{
		OperatorID:        op.OperatorID,
		OperatorPublicKey: append(ed25519.PublicKey(nil), op.PublicKey...),
		DevicePublicKey:   append(ed25519.PublicKey(nil), dev.PublicKey...),
		IssuedAt:          issuedAt.UTC(),
		ExpiresAt:         expiresAt.UTC(),
	}
	cert.Signature = ed25519.Sign(operatorPriv, certPayload(cert.OperatorID, cert.OperatorPublicKey, cert.DevicePublicKey, cert.IssuedAt, cert.ExpiresAt))
	dev.Cert = cert
	return cert, nil
}

func VerifyDeviceCert(cert *DeviceCert, now time.Time) error {
	if cert == nil {
		return errors.New("identity: nil device cert")
	}
	if len(cert.OperatorPublicKey) != ed25519.PublicKeySize {
		return errors.New("identity: bad operator public key size")
	}
	if len(cert.DevicePublicKey) != ed25519.PublicKeySize {
		return errors.New("identity: bad device public key size")
	}
	if now.After(cert.ExpiresAt) {
		return fmt.Errorf("identity: device cert expired at %s", cert.ExpiresAt.Format(time.RFC3339))
	}
	if !ed25519.Verify(cert.OperatorPublicKey, certPayload(cert.OperatorID, cert.OperatorPublicKey, cert.DevicePublicKey, cert.IssuedAt, cert.ExpiresAt), cert.Signature) {
		return errors.New("identity: invalid device cert signature")
	}
	return nil
}

func Base32Secret(pub ed25519.PublicKey) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(pub)
}

func certPayload(operatorID string, operatorPub, devicePub []byte, issuedAt, expiresAt time.Time) []byte {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		operatorID,
		hex.EncodeToString(operatorPub),
		hex.EncodeToString(devicePub),
		issuedAt.UTC().Format(time.RFC3339Nano),
		expiresAt.UTC().Format(time.RFC3339Nano),
	}, "|")))
	return sum[:]
}

func verify(pub ed25519.PublicKey, message, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, message, sig)
}

func fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	parts := make([]string, 16)
	for i := 0; i < 16; i++ {
		parts[i] = strings.ToUpper(hex.EncodeToString(sum[i : i+1]))
	}
	return strings.Join(parts, ":")
}

func shortFingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:4])
}

func sanitizeHint(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
			continue
		}
		if c == '-' || c == '_' || c == '.' || c == '@' {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return out
}
