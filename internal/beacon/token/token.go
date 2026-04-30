package token

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/beacon/identity"
)

type CapabilityToken struct {
	TokenID             string    `json:"token_id"`
	IssuerOperatorID    string    `json:"issuer_operator_id"`
	SubjectOperatorID   string    `json:"subject_operator_id"`
	BeaconIDs           []string  `json:"beacon_ids"`
	Allow               []string  `json:"allow"`
	Deny                []string  `json:"deny,omitempty"`
	CostCapUSD          float64   `json:"cost_cap_usd"`
	DelegationDepthMax  int       `json:"delegation_depth_max"`
	DelegationDepthUsed int       `json:"delegation_depth_used"`
	ConstitutionHash    string    `json:"constitution_hash"`
	ExpiresAt           time.Time `json:"expires_at"`
	ParentTokenID       string    `json:"parent_token_id,omitempty"`
	Signature           []byte    `json:"signature"`
	Version             int       `json:"schema_version"`
}

func Issue(issuer *identity.Operator, issuerPriv ed25519.PrivateKey, token CapabilityToken) (*CapabilityToken, error) {
	if issuer == nil {
		return nil, errors.New("token: issuer required")
	}
	token.IssuerOperatorID = issuer.OperatorID
	token.Version = 1
	if err := validateUnsigned(token); err != nil {
		return nil, err
	}
	if token.TokenID == "" {
		token.TokenID = fingerprint(unsignedPayload(token))
	}
	token.Signature = ed25519.Sign(issuerPriv, unsignedPayload(token))
	return &token, nil
}

func Verify(tok *CapabilityToken, issuerPub ed25519.PublicKey, now time.Time) error {
	if tok == nil {
		return errors.New("token: nil token")
	}
	if err := validateUnsigned(*tok); err != nil {
		return err
	}
	if now.After(tok.ExpiresAt) {
		return fmt.Errorf("token: expired at %s", tok.ExpiresAt.Format(time.RFC3339))
	}
	if !ed25519.Verify(issuerPub, unsignedPayload(*tok), tok.Signature) {
		return errors.New("token: invalid signature")
	}
	return nil
}

func Authorize(tok *CapabilityToken, beaconID, permission string, costUSD float64) error {
	if tok == nil {
		return errors.New("token: nil token")
	}
	if costUSD > tok.CostCapUSD {
		return fmt.Errorf("token: cost cap exceeded %.2f > %.2f", costUSD, tok.CostCapUSD)
	}
	if !matchesOne(tok.BeaconIDs, beaconID) {
		return fmt.Errorf("token: beacon %q not allowed", beaconID)
	}
	if matchesOne(tok.Deny, permission) {
		return fmt.Errorf("token: permission %q denied", permission)
	}
	if !matchesOne(tok.Allow, permission) {
		return fmt.Errorf("token: permission %q not granted", permission)
	}
	return nil
}

func Delegate(parent *CapabilityToken, issuer *identity.Operator, issuerPriv ed25519.PrivateKey, child CapabilityToken) (*CapabilityToken, error) {
	if parent == nil {
		return nil, errors.New("token: parent token required")
	}
	if parent.DelegationDepthUsed >= parent.DelegationDepthMax {
		return nil, errors.New("token: delegation depth exceeded")
	}
	child.ParentTokenID = parent.TokenID
	child.DelegationDepthMax = parent.DelegationDepthMax
	child.DelegationDepthUsed = parent.DelegationDepthUsed + 1
	if child.CostCapUSD > parent.CostCapUSD {
		return nil, errors.New("token: delegated cost cap exceeds parent")
	}
	if !subsetOf(child.BeaconIDs, parent.BeaconIDs) || !subsetOf(child.Allow, parent.Allow) {
		return nil, errors.New("token: delegated scope exceeds parent")
	}
	return Issue(issuer, issuerPriv, child)
}

func validateUnsigned(tok CapabilityToken) error {
	if tok.SubjectOperatorID == "" {
		return errors.New("token: subject_operator_id required")
	}
	if len(tok.BeaconIDs) == 0 {
		return errors.New("token: at least one beacon_id required")
	}
	if len(tok.Allow) == 0 {
		return errors.New("token: at least one permission required")
	}
	if tok.ConstitutionHash == "" {
		return errors.New("token: constitution_hash required")
	}
	if tok.ExpiresAt.IsZero() {
		return errors.New("token: expires_at required")
	}
	if tok.DelegationDepthMax < tok.DelegationDepthUsed {
		return errors.New("token: invalid delegation depth")
	}
	return nil
}

func unsignedPayload(tok CapabilityToken) []byte {
	copyTok := tok
	copyTok.Signature = nil
	b, _ := json.Marshal(copyTok)
	return b
}

func subsetOf(child, parent []string) bool {
	for _, item := range child {
		if !matchesOne(parent, item) {
			return false
		}
	}
	return true
}

func matchesOne(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if pattern == "*" || pattern == value {
			return true
		}
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(value, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

func fingerprint(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:8])
}
