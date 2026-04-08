package bench

import (
	"context"
	"encoding/json"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

// TrustMetrics captures trust rule effectiveness.
type TrustMetrics struct {
	DoneRejectionRate       float64
	FixRegressionRate       float64
	PrematureImpossibleRate float64
	TrustCostOverhead       float64
}

// ConsensusMetrics captures consensus loop behavior.
type ConsensusMetrics struct {
	AvgIterationsToConverge float64
	DissentRate             float64
	JudgeInvocationRate     float64
	ConvergenceRate         float64
}

// ComputeMetrics queries ledger and bus event log to extract bench metrics
// for a given mission.
func ComputeMetrics(ctx context.Context, l *ledger.Ledger, b *bus.Bus, missionID string) (*RunResult, error) {
	result := &RunResult{
		MissionID:     missionID,
		TerminalState: "converged",
	}

	// Count supervisor rule firings from bus events.
	missionScope := bus.Scope{MissionID: missionID}
	err := b.Replay(bus.Pattern{
		TypePrefix: "supervisor.rule.fired",
		Scope:      &missionScope,
	}, 0, func(evt bus.Event) {
		result.TrustFirings++
	})
	if err != nil {
		return nil, err
	}

	// Count worker spawns as a proxy for loop iterations.
	err = b.Replay(bus.Pattern{
		TypePrefix: "worker.spawned",
		Scope:      &missionScope,
	}, 0, func(evt bus.Event) {
		result.LoopIterations++
	})
	if err != nil {
		return nil, err
	}

	// Count escalation events.
	err = b.Replay(bus.Pattern{
		TypePrefix: "mission.aborted",
		Scope:      &missionScope,
	}, 0, func(evt bus.Event) {
		result.EscalationCount++
		result.TerminalState = "escalated"
	})
	if err != nil {
		return nil, err
	}

	// Extract cost and token data from ledger nodes.
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		MissionID: missionID,
		Type:      "cost_record",
	})
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		var rec struct {
			CostUSD    float64 `json:"cost_usd"`
			TokensUsed int64   `json:"tokens_used"`
		}
		if json.Unmarshal(n.Content, &rec) == nil {
			result.CostUSD += rec.CostUSD
			result.TokensUsed += rec.TokensUsed
		}
	}

	// Count dissent from declaration events.
	err = b.Replay(bus.Pattern{
		TypePrefix: "worker.declaration.",
		Scope:      &missionScope,
	}, 0, func(evt bus.Event) {
		if evt.Type == bus.EvtWorkerDeclarationProblem {
			result.DissentCount++
		}
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// ComputeTrustMetrics extracts trust rule effectiveness metrics from the
// ledger for a given mission.
func ComputeTrustMetrics(ctx context.Context, l *ledger.Ledger, missionID string) (*TrustMetrics, error) {
	tm := &TrustMetrics{}

	nodes, err := l.Query(ctx, ledger.QueryFilter{
		MissionID: missionID,
		Type:      "trust_evaluation",
	})
	if err != nil {
		return nil, err
	}

	var doneTotal, doneRejected int
	var fixTotal, fixRegressed int
	var impossibleTotal, impossiblePremature int

	for _, n := range nodes {
		var eval struct {
			Rule     string `json:"rule"`
			Outcome  string `json:"outcome"`
			CostUSD  float64 `json:"cost_usd"`
		}
		if json.Unmarshal(n.Content, &eval) != nil {
			continue
		}

		switch eval.Rule {
		case "done_check":
			doneTotal++
			if eval.Outcome == "rejected" {
				doneRejected++
			}
		case "fix_check":
			fixTotal++
			if eval.Outcome == "regressed" {
				fixRegressed++
			}
		case "impossible_check":
			impossibleTotal++
			if eval.Outcome == "premature" {
				impossiblePremature++
			}
		}
		tm.TrustCostOverhead += eval.CostUSD
	}

	if doneTotal > 0 {
		tm.DoneRejectionRate = float64(doneRejected) / float64(doneTotal)
	}
	if fixTotal > 0 {
		tm.FixRegressionRate = float64(fixRegressed) / float64(fixTotal)
	}
	if impossibleTotal > 0 {
		tm.PrematureImpossibleRate = float64(impossiblePremature) / float64(impossibleTotal)
	}

	return tm, nil
}

// ComputeConsensusMetrics extracts consensus loop metrics from the ledger
// for a given mission.
func ComputeConsensusMetrics(ctx context.Context, l *ledger.Ledger, missionID string) (*ConsensusMetrics, error) {
	cm := &ConsensusMetrics{}

	nodes, err := l.Query(ctx, ledger.QueryFilter{
		MissionID: missionID,
		Type:      "consensus_round",
	})
	if err != nil {
		return nil, err
	}

	if len(nodes) == 0 {
		return cm, nil
	}

	var totalIterations int
	var converged int
	var dissents int
	var judgeInvocations int

	for _, n := range nodes {
		var round struct {
			Iterations int  `json:"iterations"`
			Converged  bool `json:"converged"`
			Dissents   int  `json:"dissents"`
			JudgeUsed  bool `json:"judge_used"`
		}
		if json.Unmarshal(n.Content, &round) != nil {
			continue
		}
		totalIterations += round.Iterations
		if round.Converged {
			converged++
		}
		dissents += round.Dissents
		if round.JudgeUsed {
			judgeInvocations++
		}
	}

	total := len(nodes)
	cm.AvgIterationsToConverge = float64(totalIterations) / float64(total)
	cm.DissentRate = float64(dissents) / float64(total)
	cm.JudgeInvocationRate = float64(judgeInvocations) / float64(total)
	cm.ConvergenceRate = float64(converged) / float64(total)

	return cm, nil
}
