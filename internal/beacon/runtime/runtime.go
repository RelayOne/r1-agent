package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1/internal/artifact"
	"github.com/RelayOne/r1/internal/beacon/review"
	"github.com/RelayOne/r1/internal/beacon/trust"
	"github.com/RelayOne/r1/internal/beacon/trust/kinds"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
	"github.com/RelayOne/r1/internal/notify"
	"github.com/RelayOne/r1/internal/operator"
	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/sessionctl"
)

type ToolAdapter interface {
	RotateSessionKey(context.Context) error
	Resume(context.Context) error
	RevokeToken(context.Context, string) error
}

type AttestationSource struct {
	BuildHash               string
	ConstitutionHash        string
	LedgerRootHash          string
	ActiveTokenFingerprints []string
	Platform                string
	BeaconVersion           string
}

type Config struct {
	RepoRoot       string
	MissionID      string
	BeaconID       string
	SessionID      string
	Root           *trust.TrustRoot
	Replay         trust.ReplayStore
	Router         *sessionctl.ApprovalRouter
	Operator       operator.Operator
	Signaler       sessionctl.Signaler
	PGID           int
	Tools          ToolAdapter
	Notifier       notify.Notifier
	Attestation    AttestationSource
	CurrentActor   string
	CurrentTime    func() time.Time
	OfflineTimeout time.Duration
}

type Runtime struct {
	dispatcher *trust.Dispatcher
}

func New(cfg Config) (*Runtime, error) {
	if cfg.Root == nil {
		return nil, fmt.Errorf("beacon runtime: trust root required")
	}
	if cfg.Replay == nil {
		cfg.Replay = trust.NewMemoryReplayStore()
	}
	if cfg.CurrentTime == nil {
		cfg.CurrentTime = time.Now
	}
	if cfg.OfflineTimeout == 0 {
		cfg.OfflineTimeout = 5 * time.Minute
	}
	writer, err := newLedgerWriter(cfg)
	if err != nil {
		return nil, err
	}
	dispatcher := trust.NewDispatcher(cfg.Root, cfg.Replay, trust.Dependencies{
		UserPrompter:      userPrompter{cfg: cfg},
		SessionController: sessionController{cfg: cfg},
		ToolDispatcher:    toolDispatcher{cfg: cfg},
		Attestor:          attestor{cfg: cfg},
		Now:               cfg.CurrentTime,
	}, writer)
	kinds.RegisterAll(dispatcher)
	return &Runtime{dispatcher: dispatcher}, nil
}

func (r *Runtime) Process(ctx context.Context, frame *trust.SignalFrame) error {
	return r.dispatcher.Process(ctx, frame)
}

type userPrompter struct{ cfg Config }

func (p userPrompter) Prompt(ctx context.Context, req trust.UserPromptRequest) (trust.UserPromptResponse, error) {
	if p.cfg.Router != nil && p.cfg.Operator != nil {
		options := make([]operator.Option, 0, len(req.Choices))
		for _, choice := range req.Choices {
			options = append(options, operator.Option{Label: choice})
		}
		ch, _, err := p.cfg.Router.AskThroughRouter(ctx, p.cfg.Operator, req.Title+"\n\n"+req.Body, options, p.cfg.OfflineTimeout)
		if err != nil {
			return trust.UserPromptResponse{}, err
		}
		decision := <-ch
		return trust.UserPromptResponse{Choice: decision.Choice}, nil
	}
	if p.cfg.Operator != nil {
		options := make([]operator.Option, 0, len(req.Choices))
		for _, choice := range req.Choices {
			options = append(options, operator.Option{Label: choice})
		}
		choice, err := p.cfg.Operator.Ask(ctx, req.Title+"\n\n"+req.Body, options)
		return trust.UserPromptResponse{Choice: choice}, err
	}
	return trust.UserPromptResponse{}, nil
}

type sessionController struct{ cfg Config }

func (c sessionController) Pause(ctx context.Context, reason string) error {
	if c.cfg.Signaler == nil || c.cfg.PGID == 0 {
		return fmt.Errorf("beacon runtime: signaler unavailable")
	}
	return c.cfg.Signaler.Pause(c.cfg.PGID)
}

func (c sessionController) RotateSessionKey(ctx context.Context) error {
	if c.cfg.Tools == nil {
		return fmt.Errorf("beacon runtime: tool adapter unavailable")
	}
	return c.cfg.Tools.RotateSessionKey(ctx)
}

func (c sessionController) ForceResurgence(ctx context.Context, reason string) error {
	if c.cfg.Operator != nil {
		c.cfg.Operator.Notify(operator.KindWarn, "Beacon forced resurgence: "+reason)
	}
	return nil
}

type toolDispatcher struct{ cfg Config }

func (d toolDispatcher) Dispatch(ctx context.Context, tool string, args json.RawMessage) (trust.ToolResult, error) {
	switch tool {
	case "pause":
		if err := (sessionController{cfg: d.cfg}).Pause(ctx, "beacon trust signal"); err != nil {
			return trust.ToolResult{}, err
		}
	case "resume":
		if d.cfg.Tools == nil {
			return trust.ToolResult{}, fmt.Errorf("beacon runtime: tool adapter unavailable")
		}
		if err := d.cfg.Tools.Resume(ctx); err != nil {
			return trust.ToolResult{}, err
		}
	case "rotate_session_key":
		if err := (sessionController{cfg: d.cfg}).RotateSessionKey(ctx); err != nil {
			return trust.ToolResult{}, err
		}
	case "revoke_token":
		if d.cfg.Tools == nil {
			return trust.ToolResult{}, fmt.Errorf("beacon runtime: tool adapter unavailable")
		}
		var payload struct {
			TokenID string `json:"token_id"`
		}
		if err := json.Unmarshal(args, &payload); err != nil {
			return trust.ToolResult{}, err
		}
		if payload.TokenID == "" {
			return trust.ToolResult{}, fmt.Errorf("beacon runtime: token_id required")
		}
		if err := d.cfg.Tools.RevokeToken(ctx, payload.TokenID); err != nil {
			return trust.ToolResult{}, err
		}
	case "attest_state":
		att, err := attestor{cfg: d.cfg}.Attest(ctx)
		if err != nil {
			return trust.ToolResult{}, err
		}
		out, err := json.Marshal(att)
		if err != nil {
			return trust.ToolResult{}, err
		}
		return trust.ToolResult{OK: true, Output: out}, nil
	default:
		return trust.ToolResult{}, fmt.Errorf("beacon runtime: unsupported tool %q", tool)
	}
	return trust.ToolResult{OK: true}, nil
}

type attestor struct{ cfg Config }

func (a attestor) Attest(ctx context.Context) (trust.Attestation, error) {
	return trust.Attestation{
		BuildHash:               a.cfg.Attestation.BuildHash,
		ConstitutionHash:        a.cfg.Attestation.ConstitutionHash,
		LedgerRootHash:          a.cfg.Attestation.LedgerRootHash,
		ActiveTokenFingerprints: append([]string(nil), a.cfg.Attestation.ActiveTokenFingerprints...),
		Platform:                a.cfg.Attestation.Platform,
		BeaconVersion:           a.cfg.Attestation.BeaconVersion,
		AttestedAt:              a.cfg.CurrentTime().UTC(),
	}, nil
}

type ledgerWriter struct {
	cfg     Config
	ledger  *ledger.Ledger
	builder *artifact.Builder
}

func newLedgerWriter(cfg Config) (*ledgerWriter, error) {
	if cfg.RepoRoot == "" {
		return &ledgerWriter{cfg: cfg}, nil
	}
	ledgerDir := r1dir.JoinFor(cfg.RepoRoot, "ledger")
	lg, err := ledger.New(ledgerDir)
	if err != nil {
		return nil, err
	}
	store, err := artifact.NewStore(filepath.Join(filepath.Dir(ledgerDir), "artifacts"))
	if err != nil {
		_ = lg.Close()
		return nil, err
	}
	return &ledgerWriter{cfg: cfg, ledger: lg, builder: artifact.NewBuilder(store, lg)}, nil
}

func (w *ledgerWriter) Append(ctx context.Context, body []byte, entries []trust.LedgerEntry) ([]string, error) {
	if w.ledger == nil {
		return nil, nil
	}
	node := ledger.Node{
		Type:          "trust_signal",
		SchemaVersion: 1,
		CreatedAt:     w.cfg.CurrentTime().UTC(),
		CreatedBy:     actorOrDefault(w.cfg.CurrentActor),
		MissionID:     w.cfg.MissionID,
		Content:       body,
	}
	ids := make([]string, 0, 1+len(entries))
	id, err := w.ledger.AddNode(ctx, node)
	if err != nil {
		return nil, err
	}
	ids = append(ids, id)
	for _, entry := range entries {
		child := ledger.Node{
			Type:          entry.NodeType,
			SchemaVersion: 1,
			CreatedAt:     w.cfg.CurrentTime().UTC(),
			CreatedBy:     actorOrDefault(w.cfg.CurrentActor),
			MissionID:     w.cfg.MissionID,
			Content:       entry.Marshaled,
		}
		childID, err := w.ledger.AddNode(ctx, child)
		if err != nil {
			return nil, err
		}
		if err := w.ledger.AddEdge(ctx, ledger.Edge{From: childID, To: id, Type: ledger.EdgeReferences}); err != nil {
			return nil, err
		}
		ids = append(ids, childID)
		if entry.NodeType == "device_attestation" {
			var att nodes.DeviceAttestation
			if err := json.Unmarshal(entry.Marshaled, &att); err == nil && w.cfg.Notifier != nil {
				_ = w.cfg.Notifier.Notify(notify.NotifyEvent{
					Type:      "device_attestation",
					BeaconID:  w.cfg.BeaconID,
					SessionID: w.cfg.SessionID,
					Message:   "Beacon attestation recorded",
					Timestamp: att.AttestedAt,
				})
			}
		}
	}
	return ids, nil
}

func (w *ledgerWriter) Close() error {
	if w.ledger != nil {
		return w.ledger.Close()
	}
	return nil
}

func (w *ledgerWriter) RecordOfflineReview(ctx context.Context, artifactRef, reason string) (string, error) {
	if w.builder == nil {
		return "", nil
	}
	env := review.Envelope{
		BeaconID:    w.cfg.BeaconID,
		SessionID:   w.cfg.SessionID,
		ArtifactRef: artifactRef,
		RequestedAt: w.cfg.CurrentTime().UTC(),
		Reason:      reason,
		RequestedBy: actorOrDefault(w.cfg.CurrentActor),
	}
	payload, err := env.Marshal()
	if err != nil {
		return "", err
	}
	ann := nodes.ArtifactAnnotation{
		ArtifactRef:   artifactRef,
		AnnotatorID:   actorOrDefault(w.cfg.CurrentActor),
		AnnotatorRole: "beacon",
		Action:        "comment",
		Body:          string(payload),
		When:          w.cfg.CurrentTime().UTC(),
		Version:       1,
	}
	id, err := w.builder.EmitAnnotation(ctx, w.cfg.MissionID, ann)
	if err != nil {
		return "", err
	}
	if w.cfg.Notifier != nil {
		_ = w.cfg.Notifier.Notify(notify.NotifyEvent{
			Type:        "offline_review_requested",
			BeaconID:    w.cfg.BeaconID,
			SessionID:   w.cfg.SessionID,
			ArtifactRef: artifactRef,
			Message:     reason,
			Timestamp:   env.RequestedAt,
		})
	}
	return id, nil
}

func actorOrDefault(actor string) string {
	if actor != "" {
		return actor
	}
	return "beacon"
}
