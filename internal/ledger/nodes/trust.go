package nodes

import "time"

type TrustSignal struct {
	IssuerHubID string    `json:"issuer_hub_id"`
	Kind        string    `json:"kind"`
	Nonce       []byte    `json:"nonce"`
	Reason      string    `json:"reason"`
	IssuedAt    time.Time `json:"issued_at"`
	ReceivedAt  time.Time `json:"received_at"`
	Verdict     string    `json:"verdict"`
	Notes       string    `json:"notes,omitempty"`
	Version     int       `json:"schema_version"`
}

type HubBan struct {
	HubID      string    `json:"hub_id"`
	MatchKind  string    `json:"match_kind"`
	MatchValue string    `json:"match_value"`
	Reason     string    `json:"reason"`
	BannedAt   time.Time `json:"banned_at"`
	Version    int       `json:"schema_version"`
}

type HubCooldown struct {
	HubID       string    `json:"hub_id"`
	Subject     string    `json:"subject"`
	ActionClass string    `json:"action_class"`
	Reason      string    `json:"reason"`
	AppliedAt   time.Time `json:"applied_at"`
	Version     int       `json:"schema_version"`
}

type DeviceAttestation struct {
	BuildHash               string    `json:"build_hash"`
	ConstitutionHash        string    `json:"constitution_hash,omitempty"`
	LedgerRootHash          string    `json:"ledger_root_hash,omitempty"`
	ActiveTokenFingerprints []string  `json:"active_token_fingerprints,omitempty"`
	Platform                string    `json:"platform,omitempty"`
	BeaconVersion           string    `json:"beacon_version,omitempty"`
	AttestedAt              time.Time `json:"attested_at"`
	Version                 int       `json:"schema_version"`
}

type FederationSignal struct {
	OriginHubID string    `json:"origin_hub_id"`
	TargetHubID string    `json:"target_hub_id"`
	SignalKind  string    `json:"signal_kind"`
	Reason      string    `json:"reason,omitempty"`
	IssuedAt    time.Time `json:"issued_at"`
	Version     int       `json:"schema_version"`
}

func (n *TrustSignal) NodeType() string       { return "trust_signal" }
func (n *HubBan) NodeType() string            { return "hub_ban" }
func (n *HubCooldown) NodeType() string       { return "hub_cooldown" }
func (n *DeviceAttestation) NodeType() string { return "device_attestation" }
func (n *FederationSignal) NodeType() string  { return "federation_signal" }

func (n *TrustSignal) SchemaVersion() int       { return n.Version }
func (n *HubBan) SchemaVersion() int            { return n.Version }
func (n *HubCooldown) SchemaVersion() int       { return n.Version }
func (n *DeviceAttestation) SchemaVersion() int { return n.Version }
func (n *FederationSignal) SchemaVersion() int  { return n.Version }

func (n *TrustSignal) Validate() error {
	return validateTrustNode(n.IssuerHubID, n.IssuedAt, n.Version)
}
func (n *HubBan) Validate() error      { return validateTrustNode(n.HubID, n.BannedAt, n.Version) }
func (n *HubCooldown) Validate() error { return validateTrustNode(n.HubID, n.AppliedAt, n.Version) }
func (n *DeviceAttestation) Validate() error {
	return validateTrustNode(n.BuildHash, n.AttestedAt, n.Version)
}
func (n *FederationSignal) Validate() error {
	return validateTrustNode(n.OriginHubID, n.IssuedAt, n.Version)
}

func validateTrustNode(id string, when time.Time, version int) error {
	if id == "" || when.IsZero() || version < 1 {
		return trustNodeError("trust node: required fields missing")
	}
	return nil
}

type trustNodeError string

func (e trustNodeError) Error() string { return string(e) }

func init() {
	Register("trust_signal", func() NodeTyper { return &TrustSignal{Version: 1} })
	Register("hub_ban", func() NodeTyper { return &HubBan{Version: 1} })
	Register("hub_cooldown", func() NodeTyper { return &HubCooldown{Version: 1} })
	Register("device_attestation", func() NodeTyper { return &DeviceAttestation{Version: 1} })
	Register("federation_signal", func() NodeTyper { return &FederationSignal{Version: 1} })
}
