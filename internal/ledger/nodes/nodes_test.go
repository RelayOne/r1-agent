package nodes

import (
	"encoding/json"
	"testing"
	"time"
)

// allExpectedTypes lists every node type that must be registered.
var allExpectedTypes = []string{
	"loop", "draft", "agree", "dissent",
	"decision_internal", "decision_repo",
	"task",
	"skill", "skill_loaded", "skill_applied", "skill_import_proposal",
	"snapshot_annotation",
	"escalation", "judge_verdict", "stakeholder_directive",
	"research_request", "research_report",
	"supervisor_state_checkpoint",
	"branch_completion_proposal", "branch_completion_agreement", "branch_completion_dissent",
	"sdm_advisory",
	"memory_stored", "memory_recalled",
	"artifact", "artifact_annotation",
	"trust_signal", "hub_ban", "hub_cooldown", "device_attestation", "federation_signal",
	"beacon_claim", "beacon_device_attached", "beacon_device_revoked",
	"beacon_session_opened", "beacon_session_closed",
	"beacon_token_issued", "beacon_token_used", "beacon_token_revoked",
	"beacon_delegate_created", "beacon_command", "beacon_command_result",
	"beacon_federation_handshake",
}

func TestAllTypesRegistered(t *testing.T) {
	registered := All()
	regMap := make(map[string]bool, len(registered))
	for _, name := range registered {
		regMap[name] = true
	}
	for _, expected := range allExpectedTypes {
		if !regMap[expected] {
			t.Errorf("expected node type %q to be registered, but it was not", expected)
		}
	}
}

func TestRegistryRoundTrip(t *testing.T) {
	for _, name := range allExpectedTypes {
		n, err := New(name)
		if err != nil {
			t.Errorf("New(%q) returned error: %v", name, err)
			continue
		}
		if got := n.NodeType(); got != name {
			t.Errorf("New(%q).NodeType() = %q, want %q", name, got, name)
		}
		if got := n.SchemaVersion(); got != 1 {
			t.Errorf("New(%q).SchemaVersion() = %d, want 1", name, got)
		}
	}
}

func TestNewUnknownType(t *testing.T) {
	_, err := New("nonexistent_type")
	if err == nil {
		t.Error("New(\"nonexistent_type\") should return error")
	}
}

func TestAllReturnsSorted(t *testing.T) {
	names := All()
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("All() not sorted: %q before %q", names[i-1], names[i])
		}
	}
}

// TestNodeTyperInterface verifies that every type satisfies the interface via
// concrete pointer assignment.
func TestNodeTyperInterface(t *testing.T) {
	var _ NodeTyper = &Loop{}
	var _ NodeTyper = &Draft{}
	var _ NodeTyper = &Agree{}
	var _ NodeTyper = &Dissent{}
	var _ NodeTyper = &DecisionInternal{}
	var _ NodeTyper = &DecisionRepo{}
	var _ NodeTyper = &Task{}
	var _ NodeTyper = &Skill{}
	var _ NodeTyper = &SkillLoaded{}
	var _ NodeTyper = &SkillApplied{}
	var _ NodeTyper = &SkillImportProposal{}
	var _ NodeTyper = &SnapshotAnnotation{}
	var _ NodeTyper = &Escalation{}
	var _ NodeTyper = &JudgeVerdict{}
	var _ NodeTyper = &StakeholderDirective{}
	var _ NodeTyper = &ResearchRequest{}
	var _ NodeTyper = &ResearchReport{}
	var _ NodeTyper = &SupervisorStateCheckpoint{}
	var _ NodeTyper = &BranchCompletionProposal{}
	var _ NodeTyper = &BranchCompletionAgreement{}
	var _ NodeTyper = &BranchCompletionDissent{}
	var _ NodeTyper = &SDMAdvisory{}
	var _ NodeTyper = &MemoryStored{}
	var _ NodeTyper = &MemoryRecalled{}
	var _ NodeTyper = &Artifact{}
	var _ NodeTyper = &ArtifactAnnotation{}
	var _ NodeTyper = &TrustSignal{}
	var _ NodeTyper = &HubBan{}
	var _ NodeTyper = &HubCooldown{}
	var _ NodeTyper = &DeviceAttestation{}
	var _ NodeTyper = &FederationSignal{}
	var _ NodeTyper = &BeaconClaim{}
	var _ NodeTyper = &BeaconDeviceAttached{}
	var _ NodeTyper = &BeaconDeviceRevoked{}
	var _ NodeTyper = &BeaconSessionOpened{}
	var _ NodeTyper = &BeaconSessionClosed{}
	var _ NodeTyper = &BeaconTokenIssued{}
	var _ NodeTyper = &BeaconTokenUsed{}
	var _ NodeTyper = &BeaconTokenRevoked{}
	var _ NodeTyper = &BeaconDelegateCreated{}
	var _ NodeTyper = &BeaconCommand{}
	var _ NodeTyper = &BeaconCommandResult{}
	var _ NodeTyper = &BeaconFederationHandshake{}
}

// TestValidateRejectsEmpty verifies that every type's Validate rejects a zero-value instance.
func TestValidateRejectsEmpty(t *testing.T) {
	for _, name := range allExpectedTypes {
		n, err := New(name)
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		if err := n.Validate(); err == nil {
			t.Errorf("%q.Validate() on zero-value should return error", name)
		}
	}
}

// helper for valid timestamps
var now = time.Now()

func TestLoopValidate(t *testing.T) {
	valid := &Loop{
		State:               "proposing",
		LoopType:            "prd",
		ArtifactRef:         "draft-1",
		ConvenedPartners:    []string{"reviewer"},
		IterationCount:      1,
		ProposingStanceRole: "lead",
		TaskDAGScope:        "task-1",
		CreatedAt:           now,
		CreatedBy:           "sv-1",
		Version:             1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid Loop.Validate() = %v", err)
	}
	// Research loops allow empty convened_partners.
	research := *valid
	research.LoopType = "research"
	research.ConvenedPartners = nil
	if err := research.Validate(); err != nil {
		t.Errorf("research Loop with nil partners: %v", err)
	}
	// Invalid state.
	bad := *valid
	bad.State = "invalid"
	if err := bad.Validate(); err == nil {
		t.Error("Loop with invalid state should fail")
	}
}

func TestDraftValidate(t *testing.T) {
	valid := &Draft{
		DraftType:         "prd",
		LoopRef:           "loop-1",
		ProposingStanceID: "stance-1",
		Content:           json.RawMessage(`{"title":"test"}`),
		CreatedAt:         now,
		Version:           1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid Draft.Validate() = %v", err)
	}
	bad := *valid
	bad.Content = nil
	if err := bad.Validate(); err == nil {
		t.Error("Draft with nil content should fail")
	}
}

func TestAgreeValidate(t *testing.T) {
	valid := &Agree{
		DraftRef:           "draft-1",
		AgreeingStanceID:   "stance-1",
		AgreeingStanceRole: "reviewer",
		Reasoning:          "looks good",
		CreatedAt:          now,
		Version:            1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid Agree.Validate() = %v", err)
	}
	bad := *valid
	bad.Reasoning = ""
	if err := bad.Validate(); err == nil {
		t.Error("Agree with empty reasoning should fail")
	}
}

func TestDissentValidate(t *testing.T) {
	valid := &Dissent{
		DraftRef:             "draft-1",
		DissentingStanceID:   "stance-1",
		DissentingStanceRole: "reviewer",
		Reasoning:            "needs work",
		RequestedChange:      "fix X",
		Severity:             "blocking",
		CreatedAt:            now,
		Version:              1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid Dissent.Validate() = %v", err)
	}
	bad := *valid
	bad.Severity = "critical"
	if err := bad.Validate(); err == nil {
		t.Error("Dissent with invalid severity should fail")
	}
}

func TestDecisionInternalValidate(t *testing.T) {
	valid := &DecisionInternal{
		Who:                          []DecisionParticipant{{StanceRole: "lead", SessionID: "s1"}},
		What:                         "use approach A",
		When:                         now,
		Why:                          "because",
		WithWhatContext:              "context",
		AffectsPreviousDecisions:     []string{"dec-i-1"},
		PreviousContextsAcknowledged: []string{"The earlier decision to use SQLite was made under the assumption that write throughput would stay below 100 ops/sec. That context still holds."},
		TaskDAGScope:                 "task-1",
		LoopRef:                      "loop-1",
		Version:                      1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid DecisionInternal.Validate() = %v", err)
	}
	// Mismatched lengths.
	bad := *valid
	bad.PreviousContextsAcknowledged = nil
	if err := bad.Validate(); err == nil {
		t.Error("DecisionInternal with mismatched affects/ack lengths should fail")
	}
}

func TestDecisionInternalRejectsPlaceholderAcknowledgment(t *testing.T) {
	d := &DecisionInternal{
		Who:                          []DecisionParticipant{{StanceRole: "lead", SessionID: "s1"}},
		What:                         "use approach A",
		When:                         now,
		Why:                          "because",
		WithWhatContext:              "context",
		AffectsPreviousDecisions:     []string{"dec-i-1"},
		PreviousContextsAcknowledged: []string{"ack"},
		TaskDAGScope:                 "task-1",
		LoopRef:                      "loop-1",
		Version:                      1,
	}
	if err := d.Validate(); err == nil {
		t.Fatal("expected validation to reject placeholder acknowledgment 'ack'")
	}
}

func TestDecisionInternalRejectsShortAcknowledgment(t *testing.T) {
	d := &DecisionInternal{
		Who:                          []DecisionParticipant{{StanceRole: "lead", SessionID: "s1"}},
		What:                         "use approach A",
		When:                         now,
		Why:                          "because",
		WithWhatContext:              "context",
		AffectsPreviousDecisions:     []string{"dec-i-1"},
		PreviousContextsAcknowledged: []string{"too short"},
		TaskDAGScope:                 "task-1",
		LoopRef:                      "loop-1",
		Version:                      1,
	}
	if err := d.Validate(); err == nil {
		t.Fatal("expected validation to reject short acknowledgment")
	}
}

func TestDecisionInternalAcceptsSubstantiveAcknowledgment(t *testing.T) {
	d := &DecisionInternal{
		Who:                          []DecisionParticipant{{StanceRole: "lead", SessionID: "s1"}},
		What:                         "use approach A",
		When:                         now,
		Why:                          "because",
		WithWhatContext:              "context",
		AffectsPreviousDecisions:     []string{"dec-i-1"},
		PreviousContextsAcknowledged: []string{"The earlier decision to use SQLite was made under the assumption that write throughput would stay below 100 ops/sec. That context still holds and the decision remains valid."},
		TaskDAGScope:                 "task-1",
		LoopRef:                      "loop-1",
		Version:                      1,
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("expected substantive acknowledgment to pass, got: %v", err)
	}
}

func TestDecisionRepoRejectsPlaceholderAcknowledgment(t *testing.T) {
	d := &DecisionRepo{
		Who:                          []DecisionParticipant{{StanceRole: "lead", SessionID: "s1"}},
		What:                         "use approach A",
		When:                         now,
		Why:                          "because",
		WithWhatContext:              "context",
		AffectsPreviousDecisions:     []string{"dec-r-1"},
		PreviousContextsAcknowledged: []string{"acknowledged"},
		TaskDAGScope:                 "task-1",
		LoopRef:                      "loop-1",
		Provenance:                   "stoke_authored",
		Version:                      1,
	}
	if err := d.Validate(); err == nil {
		t.Fatal("expected validation to reject placeholder acknowledgment for stoke_authored")
	}
}

func TestDecisionRepoValidate(t *testing.T) {
	valid := &DecisionRepo{
		Who:                          []DecisionParticipant{{StanceRole: "lead", SessionID: "s1"}},
		What:                         "use approach A",
		When:                         now,
		Why:                          "because",
		WithWhatContext:              "context",
		AffectsPreviousDecisions:     nil,
		PreviousContextsAcknowledged: nil,
		TaskDAGScope:                 "task-1",
		LoopRef:                      "loop-1",
		Provenance:                   "stoke_authored",
		Version:                      1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid DecisionRepo.Validate() = %v", err)
	}
	// Inherited human tolerates partial.
	inherited := &DecisionRepo{
		Provenance: "inherited_human",
		What:       "legacy choice",
		Why:        "historical",
		Version:    1,
	}
	if err := inherited.Validate(); err != nil {
		t.Errorf("inherited_human partial DecisionRepo.Validate() = %v", err)
	}
	// Invalid provenance.
	bad := *valid
	bad.Provenance = "unknown"
	if err := bad.Validate(); err == nil {
		t.Error("DecisionRepo with invalid provenance should fail")
	}
}

func TestTaskValidate(t *testing.T) {
	valid := &Task{
		Granularity:        "ticket",
		Title:              "implement X",
		Description:        "do X",
		State:              "proposed",
		AcceptanceCriteria: []string{"tests pass"},
		CreatedAt:          now,
		CreatedBy:          "lead-1",
		Version:            1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid Task.Validate() = %v", err)
	}
	bad := *valid
	bad.Granularity = "epic"
	if err := bad.Validate(); err == nil {
		t.Error("Task with invalid granularity should fail")
	}
}

func TestSkillValidate(t *testing.T) {
	valid := &Skill{
		Name:          "trust-pattern",
		Description:   "a trust pattern",
		Applicability: []string{"go"},
		Content:       "do this",
		Provenance:    "shipped_with_stoke",
		Confidence:    "proven",
		Category:      "trust",
		CreatedAt:     now,
		CreatedBy:     "manufacturer-1",
		Version:       1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid Skill.Validate() = %v", err)
	}
	bad := *valid
	bad.Confidence = "unknown"
	if err := bad.Validate(); err == nil {
		t.Error("Skill with invalid confidence should fail")
	}
}

func TestSkillLoadedValidate(t *testing.T) {
	valid := &SkillLoaded{
		SkillRef:              "skill-1",
		LoadingStanceID:       "stance-1",
		LoadingStanceRole:     "dev",
		ConcernFieldTemplate:  "dev_implementing_ticket",
		MatchingApplicability: "go files",
		TaskDAGScope:          "task-1",
		LoopRef:               "loop-1",
		CreatedAt:             now,
		Version:               1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid SkillLoaded.Validate() = %v", err)
	}
	bad := *valid
	bad.SkillRef = ""
	if err := bad.Validate(); err == nil {
		t.Error("SkillLoaded with empty skill_ref should fail")
	}
}

func TestSkillAppliedValidate(t *testing.T) {
	valid := &SkillApplied{
		SkillRef:           "skill-1",
		ApplyingStanceID:   "stance-1",
		ApplyingStanceRole: "dev",
		ArtifactRef:        "draft-1",
		HowApplied:         "used pattern X instead of Y",
		LoadRef:            "sk-load-1",
		CreatedAt:          now,
		Version:            1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid SkillApplied.Validate() = %v", err)
	}
	bad := *valid
	bad.HowApplied = ""
	if err := bad.Validate(); err == nil {
		t.Error("SkillApplied with empty how_applied should fail")
	}
}

func TestSkillImportProposalValidate(t *testing.T) {
	valid := &SkillImportProposal{
		ProposingStanceID: "stance-1",
		CandidateContent:  "---\nname: test\n---\nbody",
		SourceMetadata:    "url: https://example.com",
		ReputationSummary: "well-known source",
		SecurityReview:    "no issues found",
		ConsistencyReview: "no conflicts",
		RiskAssessment:    "low",
		TaskContext:       "implementing feature Y",
		CreatedAt:         now,
		Version:           1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid SkillImportProposal.Validate() = %v", err)
	}
	bad := *valid
	bad.RiskAssessment = "critical"
	if err := bad.Validate(); err == nil {
		t.Error("SkillImportProposal with invalid risk_assessment should fail")
	}
}

func TestSnapshotAnnotationValidate(t *testing.T) {
	valid := &SnapshotAnnotation{
		Target:         "pkg/foo/bar.go",
		AnnotationType: "intentional_pattern",
		Description:    "this pattern is load-bearing",
		Evidence:       "git history shows deliberate introduction",
		CreatedAt:      now,
		CreatedBy:      "cto-1",
		Version:        1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid SnapshotAnnotation.Validate() = %v", err)
	}
	bad := *valid
	bad.AnnotationType = "unknown"
	if err := bad.Validate(); err == nil {
		t.Error("SnapshotAnnotation with invalid annotation_type should fail")
	}
}

func TestEscalationValidate(t *testing.T) {
	valid := &Escalation{
		EscalationType:      "blocked",
		OriginatingLoopRef:  "loop-1",
		Target:              "parent_supervisor",
		Context:             "loop stuck on dissent",
		RequestedResolution: "architectural decision needed",
		CreatedAt:           now,
		CreatedBy:           "sv-1",
		Version:             1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid Escalation.Validate() = %v", err)
	}
	bad := *valid
	bad.Target = "unknown"
	if err := bad.Validate(); err == nil {
		t.Error("Escalation with invalid target should fail")
	}
}

func TestJudgeVerdictValidate(t *testing.T) {
	valid := &JudgeVerdict{
		InvokingRule:                "consensus.iteration.threshold",
		LoopRef:                     "loop-1",
		Verdict:                     "keep_iterating",
		Reasoning:                   "still making progress",
		LoopHistoryConsulted:        []string{"draft-1", "dissent-1"},
		OriginalIntentAtInvocation:  "build feature X",
		CreatedAt:                   now,
		CreatedBy:                   "judge-1",
		Version:                     1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid JudgeVerdict.Validate() = %v", err)
	}
	bad := *valid
	bad.Verdict = "reject"
	if err := bad.Validate(); err == nil {
		t.Error("JudgeVerdict with invalid verdict should fail")
	}
}

func TestStakeholderDirectiveValidate(t *testing.T) {
	valid := &StakeholderDirective{
		EscalationRef:                        "esc-1",
		StakeholderStanceID:                  "stakeholder-1",
		PostureApplied:                       "balanced",
		DirectiveType:                        "proceed_as_proposed",
		DirectiveContent:                     "continue with current approach",
		Reasoning:                            "the approach is sound",
		EvaluationSummary:                    "reviewed escalation context",
		PriorStakeholderDirectivesConsidered: nil,
		OriginalIntentAtEvaluation:           "build feature X",
		CreatedAt:                            now,
		CreatedBy:                            "stakeholder-1",
		Version:                              1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid StakeholderDirective.Validate() = %v", err)
	}
	// Second stakeholder dissent forces forward_to_user.
	bad := *valid
	bad.SecondStakeholderDissentRef = "dissent-2"
	if err := bad.Validate(); err == nil {
		t.Error("StakeholderDirective with second_stakeholder_dissent_ref and non-forward_to_user should fail")
	}
	// Same with forward_to_user should pass.
	bad.DirectiveType = "forward_to_user"
	if err := bad.Validate(); err != nil {
		t.Errorf("StakeholderDirective with forward_to_user and dissent ref: %v", err)
	}
}

func TestResearchRequestValidate(t *testing.T) {
	valid := &ResearchRequest{
		RequestingStanceID:           "stance-1",
		RequestingStanceRole:         "dev",
		Question:                     "how does X work?",
		ContextForQuestion:           "implementing feature Y",
		Audience:                     "dev",
		Urgency:                      "medium",
		ParallelResearchersRequested: 1,
		CreatedAt:                    now,
		Version:                      1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid ResearchRequest.Validate() = %v", err)
	}
	bad := *valid
	bad.ParallelResearchersRequested = 0
	if err := bad.Validate(); err == nil {
		t.Error("ResearchRequest with 0 parallel_researchers_requested should fail")
	}
}

func TestResearchReportValidate(t *testing.T) {
	valid := &ResearchReport{
		RequestRef:            "req-1",
		ResearcherStanceID:    "researcher-1",
		QuestionBeingAnswered: "how does X work?",
		SourcesCited:          []string{"https://example.com"},
		Conclusion:            "X works by doing Y",
		ConfidenceLevel:       "high",
		Limitations:           "only tested on Linux",
		CreatedAt:             now,
		Version:               1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid ResearchReport.Validate() = %v", err)
	}
	bad := *valid
	bad.SourcesCited = nil
	if err := bad.Validate(); err == nil {
		t.Error("ResearchReport with empty sources_cited should fail")
	}
}

func TestSupervisorStateCheckpointValidate(t *testing.T) {
	valid := &SupervisorStateCheckpoint{
		SupervisorInstanceID: "sv-1",
		SupervisorConfig:     "mission",
		BusCursor:            42,
		ActiveLoops:          []string{"loop-1"},
		CreatedAt:            now,
		Version:              1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid SupervisorStateCheckpoint.Validate() = %v", err)
	}
	bad := *valid
	bad.SupervisorConfig = "invalid"
	if err := bad.Validate(); err == nil {
		t.Error("SupervisorStateCheckpoint with invalid supervisor_config should fail")
	}
}

func TestBranchCompletionProposalValidate(t *testing.T) {
	valid := &BranchCompletionProposal{
		BranchSupervisorID:  "sv-branch-1",
		BranchTaskRef:       "task-branch-1",
		MissionSupervisorID: "sv-mission-1",
		SummaryOfWork:       "completed all tickets",
		UnresolvedConcerns:  "none",
		CreatedAt:           now,
		Version:             1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid BranchCompletionProposal.Validate() = %v", err)
	}
	bad := *valid
	bad.SummaryOfWork = ""
	if err := bad.Validate(); err == nil {
		t.Error("BranchCompletionProposal with empty summary_of_work should fail")
	}
}

func TestBranchCompletionAgreementValidate(t *testing.T) {
	valid := &BranchCompletionAgreement{
		ProposalRef:         "bcp-1",
		MissionSupervisorID: "sv-mission-1",
		AgreementReasoning:  "all checks passed",
		CreatedAt:           now,
		Version:             1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid BranchCompletionAgreement.Validate() = %v", err)
	}
	bad := *valid
	bad.AgreementReasoning = ""
	if err := bad.Validate(); err == nil {
		t.Error("BranchCompletionAgreement with empty agreement_reasoning should fail")
	}
}

func TestBranchCompletionDissentValidate(t *testing.T) {
	valid := &BranchCompletionDissent{
		ProposalRef:         "bcp-1",
		MissionSupervisorID: "sv-mission-1",
		DissentReasoning:    "tests not passing",
		RequestedActions:    []string{"fix failing tests"},
		CreatedAt:           now,
		Version:             1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid BranchCompletionDissent.Validate() = %v", err)
	}
	bad := *valid
	bad.RequestedActions = nil
	if err := bad.Validate(); err == nil {
		t.Error("BranchCompletionDissent with empty requested_actions should fail")
	}
}

func TestSDMAdvisoryValidate(t *testing.T) {
	valid := &SDMAdvisory{
		AdvisoryType:          "collision_file_modification",
		DetectedCondition:     "branches A and B modifying same file",
		BranchesInvolved:      []string{"task-branch-1", "task-branch-2"},
		AffectedWorkers:       []string{"worker-1"},
		SuggestedCoordination: "sync before proceeding",
		OriginatingEventRef:   "evt-42",
		CreatedAt:             now,
		Version:               1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid SDMAdvisory.Validate() = %v", err)
	}
	bad := *valid
	bad.AdvisoryType = "unknown"
	if err := bad.Validate(); err == nil {
		t.Error("SDMAdvisory with invalid advisory_type should fail")
	}
	// Valid optional severity.
	withSev := *valid
	withSev.Severity = "urgent_block"
	if err := withSev.Validate(); err != nil {
		t.Errorf("SDMAdvisory with valid severity: %v", err)
	}
	// Invalid optional severity.
	badSev := *valid
	badSev.Severity = "invalid"
	if err := badSev.Validate(); err == nil {
		t.Error("SDMAdvisory with invalid severity should fail")
	}
}
