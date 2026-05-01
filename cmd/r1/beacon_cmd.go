package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/beacon/identity"
	"github.com/RelayOne/r1/internal/beacon/pairing"
	"github.com/RelayOne/r1/internal/beacon/session"
	"github.com/RelayOne/r1/internal/beacon/token"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
	"github.com/RelayOne/r1/internal/r1dir"
)

func beaconCmd(args []string) {
	if len(args) == 0 {
		fatal("usage: r1 beacon <claimme|revoke-device|token>")
	}
	switch args[0] {
	case "claimme":
		beaconClaimmeCmd(args[1:])
	case "revoke-device":
		beaconRevokeDeviceCmd(args[1:])
	case "token":
		beaconTokenCmd(args[1:])
	default:
		fatal("unknown beacon verb %q", args[0])
	}
}

func beaconClaimmeCmd(args []string) {
	fs := flag.NewFlagSet("beacon claimme", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	missionID := fs.String("mission", "beacon", "Mission ID for ledger writes")
	host := fs.String("beacon-host", "local-beacon", "Beacon host hint")
	constitutionHash := fs.String("constitution-hash", "dev", "Constitution hash")
	operatorEmail := fs.String("operator-email", "", "Operator email hint")
	deviceKind := fs.String("device-kind", "mobile", "Device kind")
	deviceLabel := fs.String("device-label", "", "Device label")
	sas := fs.String("sas", "", "Expected SAS to verify")
	expires := fs.Duration("cert-ttl", 24*time.Hour, "Device certificate lifetime")
	fs.Parse(args)

	beaconID, beaconPriv, operatorID, operatorPriv, deviceID, _, challenge, response, derivedSAS := buildClaimFlow(*host, *constitutionHash, *operatorEmail, *deviceKind, *deviceLabel)
	if *sas != "" && *sas != derivedSAS {
		fatal("beacon claimme: SAS mismatch got=%s want=%s", derivedSAS, *sas)
	}
	cert, err := identity.SignDeviceCert(operatorID, operatorPriv, deviceID, time.Now().UTC(), time.Now().UTC().Add(*expires))
	if err != nil {
		fatal("beacon claimme: %v", err)
	}
	if err := identity.VerifyDeviceCert(cert, time.Now().UTC()); err != nil {
		fatal("beacon claimme: %v", err)
	}

	absRepo, _ := filepath.Abs(*repo)
	lg, err := ledger.New(r1dir.JoinFor(absRepo, "ledger"))
	if err != nil {
		fatal("beacon claimme: %v", err)
	}
	defer lg.Close()
	claimNode := nodes.BeaconClaim{
		BeaconID:          beaconID.BeaconID,
		OperatorID:        operatorID.OperatorID,
		DeviceFingerprint: deviceID.Fingerprint(),
		ChallengeNonceHex: hex.EncodeToString(challenge.Nonce),
		SASVerified:       true,
		ClaimedAt:         time.Now().UTC(),
		Version:           1,
	}
	claimID := appendBeaconNode(context.Background(), lg, *missionID, operatorID.OperatorID, "beacon_claim", claimNode)
	attachNode := nodes.BeaconDeviceAttached{
		BeaconID:          beaconID.BeaconID,
		OperatorID:        operatorID.OperatorID,
		DeviceFingerprint: deviceID.Fingerprint(),
		DeviceLabel:       deviceID.Label,
		DeviceKind:        deviceID.Kind,
		AttachedAt:        time.Now().UTC(),
		Version:           1,
	}
	attachID := appendBeaconNode(context.Background(), lg, *missionID, operatorID.OperatorID, "beacon_device_attached", attachNode)
	if err := lg.AddEdge(context.Background(), ledger.Edge{From: attachID, To: claimID, Type: ledger.EdgeReferences}); err != nil {
		fatal("beacon claimme: %v", err)
	}

	out := map[string]any{
		"beacon":             beaconID,
		"beacon_private_key": hex.EncodeToString(beaconPriv),
		"operator":           operatorID,
		"device":             deviceID,
		"challenge_words":    challenge.Words,
		"challenge_nonce":    hex.EncodeToString(challenge.Nonce),
		"response_nonce":     hex.EncodeToString(response.Nonce),
		"sas":                derivedSAS,
		"cert":               cert,
		"claim_node_id":      claimID,
		"attach_node_id":     attachID,
	}
	printJSON(out)
}

func beaconRevokeDeviceCmd(args []string) {
	fs := flag.NewFlagSet("beacon revoke-device", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	missionID := fs.String("mission", "beacon", "Mission ID for ledger writes")
	beaconID := fs.String("beacon-id", "", "Beacon ID")
	operatorID := fs.String("operator-id", "", "Operator ID")
	deviceFingerprint := fs.String("device-fingerprint", "", "Device fingerprint")
	reason := fs.String("reason", "", "Revocation reason")
	fs.Parse(args)
	if *beaconID == "" || *operatorID == "" || *deviceFingerprint == "" || *reason == "" {
		fatal("usage: r1 beacon revoke-device --beacon-id ID --operator-id ID --device-fingerprint FP --reason STR")
	}
	absRepo, _ := filepath.Abs(*repo)
	lg, err := ledger.New(r1dir.JoinFor(absRepo, "ledger"))
	if err != nil {
		fatal("beacon revoke-device: %v", err)
	}
	defer lg.Close()
	node := nodes.BeaconDeviceRevoked{
		BeaconID:          *beaconID,
		OperatorID:        *operatorID,
		DeviceFingerprint: *deviceFingerprint,
		Reason:            *reason,
		RevokedAt:         time.Now().UTC(),
		Version:           1,
	}
	fmt.Println(appendBeaconNode(context.Background(), lg, *missionID, *operatorID, "beacon_device_revoked", node))
}

func beaconTokenCmd(args []string) {
	if len(args) == 0 {
		fatal("usage: r1 beacon token <issue|import>")
	}
	switch args[0] {
	case "issue":
		beaconTokenIssueCmd(args[1:])
	case "import":
		beaconTokenImportCmd(args[1:])
	default:
		fatal("unknown beacon token verb %q", args[0])
	}
}

func beaconTokenIssueCmd(args []string) {
	fs := flag.NewFlagSet("beacon token issue", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	missionID := fs.String("mission", "beacon", "Mission ID for ledger writes")
	beaconID := fs.String("beacon-id", "", "Beacon ID")
	subject := fs.String("subject-operator-id", "", "Subject operator ID")
	issuerEmail := fs.String("issuer-email", "", "Issuer email hint")
	allow := fs.String("allow", "pause", "Comma-separated allow list")
	deny := fs.String("deny", "", "Comma-separated deny list")
	constitutionHash := fs.String("constitution-hash", "dev", "Constitution hash")
	costCap := fs.Float64("cost-cap-usd", 10, "Cost cap")
	ttl := fs.Duration("ttl", time.Hour, "Token lifetime")
	fs.Parse(args)
	if *beaconID == "" || *subject == "" {
		fatal("usage: r1 beacon token issue --beacon-id ID --subject-operator-id ID [flags]")
	}
	issuer, issuerPriv, err := identity.NewOperator(*issuerEmail)
	if err != nil {
		fatal("beacon token issue: %v", err)
	}
	tok, err := token.Issue(issuer, issuerPriv, token.CapabilityToken{
		SubjectOperatorID:  *subject,
		BeaconIDs:          csv(*beaconID),
		Allow:              csv(*allow),
		Deny:               csv(*deny),
		CostCapUSD:         *costCap,
		DelegationDepthMax: 2,
		ConstitutionHash:   *constitutionHash,
		ExpiresAt:          time.Now().UTC().Add(*ttl),
	})
	if err != nil {
		fatal("beacon token issue: %v", err)
	}
	absRepo, _ := filepath.Abs(*repo)
	lg, err := ledger.New(r1dir.JoinFor(absRepo, "ledger"))
	if err != nil {
		fatal("beacon token issue: %v", err)
	}
	defer lg.Close()
	node := nodes.BeaconTokenIssued{
		BeaconID:          *beaconID,
		IssuerOperatorID:  issuer.OperatorID,
		SubjectOperatorID: *subject,
		TokenID:           tok.TokenID,
		Permissions:       tok.Allow,
		ConstitutionHash:  tok.ConstitutionHash,
		ExpiresAt:         tok.ExpiresAt,
		Version:           1,
	}
	nodeID := appendBeaconNode(context.Background(), lg, *missionID, issuer.OperatorID, "beacon_token_issued", node)
	printJSON(map[string]any{
		"issuer":         issuer,
		"issuer_pub_key": hex.EncodeToString(issuer.PublicKey),
		"token":          tok,
		"ledger_node_id": nodeID,
	})
}

func beaconTokenImportCmd(args []string) {
	fs := flag.NewFlagSet("beacon token import", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	missionID := fs.String("mission", "beacon", "Mission ID for ledger writes")
	operatorID := fs.String("operator-id", "", "Operator importing the token")
	fs.Parse(args)
	if fs.NArg() != 1 || *operatorID == "" {
		fatal("usage: r1 beacon token import --operator-id ID <token.json>")
	}
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fatal("beacon token import: %v", err)
	}
	var tok token.CapabilityToken
	if err := json.Unmarshal(data, &tok); err != nil {
		fatal("beacon token import: %v", err)
	}
	if len(tok.BeaconIDs) == 0 {
		fatal("beacon token import: token missing beacon ids")
	}
	absRepo, _ := filepath.Abs(*repo)
	lg, err := ledger.New(r1dir.JoinFor(absRepo, "ledger"))
	if err != nil {
		fatal("beacon token import: %v", err)
	}
	defer lg.Close()
	node := nodes.BeaconTokenUsed{
		BeaconID:   tok.BeaconIDs[0],
		OperatorID: *operatorID,
		TokenID:    tok.TokenID,
		Permission: "import",
		UsedAt:     time.Now().UTC(),
		Version:    1,
	}
	fmt.Println(appendBeaconNode(context.Background(), lg, *missionID, *operatorID, "beacon_token_used", node))
}

func buildClaimFlow(host, constitutionHash, operatorEmail, deviceKind, deviceLabel string) (*identity.Beacon, ed25519.PrivateKey, *identity.Operator, ed25519.PrivateKey, *identity.Device, ed25519.PrivateKey, *pairing.Challenge, *pairing.Response, string) {
	beaconID, beaconPriv, err := identity.NewBeacon(host, constitutionHash)
	if err != nil {
		fatal("beacon claimme: %v", err)
	}
	operatorID, operatorPriv, err := identity.NewOperator(operatorEmail)
	if err != nil {
		fatal("beacon claimme: %v", err)
	}
	deviceID, devicePriv, err := identity.NewDevice(deviceKind, deviceLabel)
	if err != nil {
		fatal("beacon claimme: %v", err)
	}
	beaconEphemeral, err := pairingEphemeral()
	if err != nil {
		fatal("beacon claimme: %v", err)
	}
	deviceEphemeral, err := pairingEphemeral()
	if err != nil {
		fatal("beacon claimme: %v", err)
	}
	challenge, err := pairing.NewChallenge(beaconID, beaconEphemeral)
	if err != nil {
		fatal("beacon claimme: %v", err)
	}
	response, err := pairing.NewResponse(challenge, deviceID, deviceEphemeral, operatorID.PublicKey)
	if err != nil {
		fatal("beacon claimme: %v", err)
	}
	derivedSAS, err := pairing.VerifySAS(challenge, response)
	if err != nil {
		fatal("beacon claimme: %v", err)
	}
	return beaconID, beaconPriv, operatorID, operatorPriv, deviceID, devicePriv, challenge, response, derivedSAS
}

func pairingEphemeral() ([]byte, error) {
	keys, err := session.NewEphemeralKeypair()
	if err != nil {
		return nil, err
	}
	return keys.PublicKey, nil
}

func appendBeaconNode(ctx context.Context, lg *ledger.Ledger, missionID, createdBy, nodeType string, payload any) string {
	body, err := json.Marshal(payload)
	if err != nil {
		fatal("beacon: marshal %s: %v", nodeType, err)
	}
	id, err := lg.AddNode(ctx, ledger.Node{
		Type:          nodeType,
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     createdBy,
		MissionID:     missionID,
		Content:       body,
	})
	if err != nil {
		fatal("beacon: add %s: %v", nodeType, err)
	}
	return id
}

func csv(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fatal("encode json: %v", err)
	}
}
