package nodes

import (
	"encoding/json"
	"fmt"
	"time"
)

type BeaconClaim struct {
	BeaconID          string    `json:"beacon_id"`
	OperatorID        string    `json:"operator_id"`
	DeviceFingerprint string    `json:"device_fingerprint"`
	ChallengeNonceHex string    `json:"challenge_nonce_hex"`
	SASVerified       bool      `json:"sas_verified"`
	ClaimedAt         time.Time `json:"claimed_at"`
	Version           int       `json:"schema_version"`
}

type BeaconDeviceAttached struct {
	BeaconID          string    `json:"beacon_id"`
	OperatorID        string    `json:"operator_id"`
	DeviceFingerprint string    `json:"device_fingerprint"`
	DeviceLabel       string    `json:"device_label"`
	DeviceKind        string    `json:"device_kind"`
	AttachedAt        time.Time `json:"attached_at"`
	Version           int       `json:"schema_version"`
}

type BeaconDeviceRevoked struct {
	BeaconID          string    `json:"beacon_id"`
	OperatorID        string    `json:"operator_id"`
	DeviceFingerprint string    `json:"device_fingerprint"`
	Reason            string    `json:"reason"`
	RevokedAt         time.Time `json:"revoked_at"`
	Version           int       `json:"schema_version"`
}

type BeaconSessionOpened struct {
	BeaconID          string    `json:"beacon_id"`
	OperatorID        string    `json:"operator_id"`
	DeviceFingerprint string    `json:"device_fingerprint"`
	SessionID         string    `json:"session_id"`
	TokenID           string    `json:"token_id,omitempty"`
	OpenedAt          time.Time `json:"opened_at"`
	Version           int       `json:"schema_version"`
}

type BeaconSessionClosed struct {
	BeaconID   string    `json:"beacon_id"`
	OperatorID string    `json:"operator_id"`
	SessionID  string    `json:"session_id"`
	Reason     string    `json:"reason"`
	ClosedAt   time.Time `json:"closed_at"`
	Version    int       `json:"schema_version"`
}

type BeaconTokenIssued struct {
	BeaconID          string    `json:"beacon_id"`
	IssuerOperatorID  string    `json:"issuer_operator_id"`
	SubjectOperatorID string    `json:"subject_operator_id"`
	TokenID           string    `json:"token_id"`
	Permissions       []string  `json:"permissions"`
	ConstitutionHash  string    `json:"constitution_hash"`
	ExpiresAt         time.Time `json:"expires_at"`
	Version           int       `json:"schema_version"`
}

type BeaconTokenUsed struct {
	BeaconID   string    `json:"beacon_id"`
	OperatorID string    `json:"operator_id"`
	TokenID    string    `json:"token_id"`
	Permission string    `json:"permission"`
	UsedAt     time.Time `json:"used_at"`
	Version    int       `json:"schema_version"`
}

type BeaconTokenRevoked struct {
	BeaconID   string    `json:"beacon_id"`
	OperatorID string    `json:"operator_id"`
	TokenID    string    `json:"token_id"`
	Reason     string    `json:"reason"`
	RevokedAt  time.Time `json:"revoked_at"`
	Version    int       `json:"schema_version"`
}

type BeaconDelegateCreated struct {
	BeaconID      string    `json:"beacon_id"`
	OperatorID    string    `json:"operator_id"`
	ParentTokenID string    `json:"parent_token_id"`
	ChildTokenID  string    `json:"child_token_id"`
	CreatedAt     time.Time `json:"created_at"`
	Version       int       `json:"schema_version"`
}

type BeaconCommand struct {
	BeaconID       string          `json:"beacon_id"`
	OperatorID     string          `json:"operator_id"`
	SessionID      string          `json:"session_id"`
	CommandKind    string          `json:"command_kind"`
	CommandPayload json.RawMessage `json:"command_payload"`
	IssuedAt       time.Time       `json:"issued_at"`
	Version        int             `json:"schema_version"`
}

type BeaconCommandResult struct {
	BeaconID      string          `json:"beacon_id"`
	OperatorID    string          `json:"operator_id"`
	CommandNodeID string          `json:"command_node_id"`
	Status        string          `json:"status"`
	ResultPayload json.RawMessage `json:"result_payload,omitempty"`
	CompletedAt   time.Time       `json:"completed_at"`
	Version       int             `json:"schema_version"`
}

type BeaconFederationHandshake struct {
	BeaconID    string    `json:"beacon_id"`
	OriginHubID string    `json:"origin_hub_id"`
	PeerHubID   string    `json:"peer_hub_id"`
	SessionID   string    `json:"session_id"`
	HandshakeAt time.Time `json:"handshake_at"`
	Version     int       `json:"schema_version"`
}

func (n *BeaconClaim) NodeType() string               { return "beacon_claim" }
func (n *BeaconDeviceAttached) NodeType() string      { return "beacon_device_attached" }
func (n *BeaconDeviceRevoked) NodeType() string       { return "beacon_device_revoked" }
func (n *BeaconSessionOpened) NodeType() string       { return "beacon_session_opened" }
func (n *BeaconSessionClosed) NodeType() string       { return "beacon_session_closed" }
func (n *BeaconTokenIssued) NodeType() string         { return "beacon_token_issued" }
func (n *BeaconTokenUsed) NodeType() string           { return "beacon_token_used" }
func (n *BeaconTokenRevoked) NodeType() string        { return "beacon_token_revoked" }
func (n *BeaconDelegateCreated) NodeType() string     { return "beacon_delegate_created" }
func (n *BeaconCommand) NodeType() string             { return "beacon_command" }
func (n *BeaconCommandResult) NodeType() string       { return "beacon_command_result" }
func (n *BeaconFederationHandshake) NodeType() string { return "beacon_federation_handshake" }

func (n *BeaconClaim) SchemaVersion() int               { return n.Version }
func (n *BeaconDeviceAttached) SchemaVersion() int      { return n.Version }
func (n *BeaconDeviceRevoked) SchemaVersion() int       { return n.Version }
func (n *BeaconSessionOpened) SchemaVersion() int       { return n.Version }
func (n *BeaconSessionClosed) SchemaVersion() int       { return n.Version }
func (n *BeaconTokenIssued) SchemaVersion() int         { return n.Version }
func (n *BeaconTokenUsed) SchemaVersion() int           { return n.Version }
func (n *BeaconTokenRevoked) SchemaVersion() int        { return n.Version }
func (n *BeaconDelegateCreated) SchemaVersion() int     { return n.Version }
func (n *BeaconCommand) SchemaVersion() int             { return n.Version }
func (n *BeaconCommandResult) SchemaVersion() int       { return n.Version }
func (n *BeaconFederationHandshake) SchemaVersion() int { return n.Version }

func (n *BeaconClaim) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.OperatorID, n.ClaimedAt, n.Version)
}
func (n *BeaconDeviceAttached) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.OperatorID, n.AttachedAt, n.Version)
}
func (n *BeaconDeviceRevoked) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.OperatorID, n.RevokedAt, n.Version)
}
func (n *BeaconSessionOpened) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.OperatorID, n.OpenedAt, n.Version)
}
func (n *BeaconSessionClosed) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.OperatorID, n.ClosedAt, n.Version)
}
func (n *BeaconTokenIssued) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.IssuerOperatorID, n.ExpiresAt, n.Version)
}
func (n *BeaconTokenUsed) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.OperatorID, n.UsedAt, n.Version)
}
func (n *BeaconTokenRevoked) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.OperatorID, n.RevokedAt, n.Version)
}
func (n *BeaconDelegateCreated) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.OperatorID, n.CreatedAt, n.Version)
}
func (n *BeaconCommand) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.OperatorID, n.IssuedAt, n.Version)
}
func (n *BeaconCommandResult) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.OperatorID, n.CompletedAt, n.Version)
}
func (n *BeaconFederationHandshake) Validate() error {
	return validateBeaconTimes(n.BeaconID, n.OriginHubID, n.HandshakeAt, n.Version)
}

func validateBeaconTimes(beaconID, actorID string, when time.Time, version int) error {
	if beaconID == "" {
		return fmt.Errorf("beacon node: beacon_id required")
	}
	if actorID == "" {
		return fmt.Errorf("beacon node: actor id required")
	}
	if when.IsZero() {
		return fmt.Errorf("beacon node: timestamp required")
	}
	if version < 1 {
		return fmt.Errorf("beacon node: schema_version must be >= 1")
	}
	return nil
}

func init() {
	Register("beacon_claim", func() NodeTyper { return &BeaconClaim{Version: 1} })
	Register("beacon_device_attached", func() NodeTyper { return &BeaconDeviceAttached{Version: 1} })
	Register("beacon_device_revoked", func() NodeTyper { return &BeaconDeviceRevoked{Version: 1} })
	Register("beacon_session_opened", func() NodeTyper { return &BeaconSessionOpened{Version: 1} })
	Register("beacon_session_closed", func() NodeTyper { return &BeaconSessionClosed{Version: 1} })
	Register("beacon_token_issued", func() NodeTyper { return &BeaconTokenIssued{Version: 1} })
	Register("beacon_token_used", func() NodeTyper { return &BeaconTokenUsed{Version: 1} })
	Register("beacon_token_revoked", func() NodeTyper { return &BeaconTokenRevoked{Version: 1} })
	Register("beacon_delegate_created", func() NodeTyper { return &BeaconDelegateCreated{Version: 1} })
	Register("beacon_command", func() NodeTyper { return &BeaconCommand{Version: 1} })
	Register("beacon_command_result", func() NodeTyper { return &BeaconCommandResult{Version: 1} })
	Register("beacon_federation_handshake", func() NodeTyper { return &BeaconFederationHandshake{Version: 1} })
}
