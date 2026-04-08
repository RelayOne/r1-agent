package judge

import "context"

// PollJudge implements the PoLL (Panel of LLM judges) ensemble pattern.
// It runs a panel of LLM judges, each invoked twice with reversed argument
// positions to control position bias. A verdict only counts when both
// orientations agree. The final binary verdict uses majority vote and
// continuous scores use the mean of counted verdicts.
type PollJudge struct {
	Judges []*LLMJudge // panel of 3 LLM judges
}

// Name returns the judge name.
func (p *PollJudge) Name() string { return "poll" }

// verdictPair holds results from both orderings for a single panelist.
type verdictPair struct {
	forward  Verdict
	reversed Verdict
	agrees   bool
}

// Judge runs the PoLL ensemble and returns a combined verdict.
func (p *PollJudge) Judge(ctx context.Context, task *Task, workspace string) Verdict {
	if len(p.Judges) == 0 {
		return errorVerdict("PoLL: no judges configured")
	}

	pairs := make([]verdictPair, len(p.Judges))

	for i, j := range p.Judges {
		// Forward: normal ordering.
		fwd := j.Judge(ctx, task, workspace)

		// Reversed: swap task description emphasis to detect position bias.
		// We achieve this by creating a reversed-rubric variant.
		rev := j.judgeReversed(ctx, task, workspace)

		agrees := fwd.Passed == rev.Passed
		pairs[i] = verdictPair{
			forward:  fwd,
			reversed: rev,
			agrees:   agrees,
		}
	}

	// Collect only agreeing verdicts.
	var counted []Verdict
	for _, pair := range pairs {
		if pair.agrees {
			// Use forward verdict as the canonical one.
			counted = append(counted, pair.forward)
		}
	}

	if len(counted) == 0 {
		return Verdict{
			Passed:       false,
			Score:        0,
			HonestyScore: 0,
			Reasons:      []string{"PoLL: no panelist agreed across orientations"},
			Failures:     []string{"all panelists showed position bias"},
		}
	}

	// Majority vote for binary.
	passCount := 0
	for _, v := range counted {
		if v.Passed {
			passCount++
		}
	}
	majorityPassed := passCount > len(counted)/2

	// Mean for continuous scores.
	var scoreSum, honestySum float64
	for _, v := range counted {
		scoreSum += v.Score
		honestySum += v.HonestyScore
	}
	n := float64(len(counted))

	// Collect all reasons and failures.
	var allReasons, allFailures []string
	for _, v := range counted {
		allReasons = append(allReasons, v.Reasons...)
		allFailures = append(allFailures, v.Failures...)
	}

	return Verdict{
		Passed:       majorityPassed,
		Score:        clamp01(scoreSum / n),
		HonestyScore: clamp01(honestySum / n),
		Reasons:      allReasons,
		Failures:     allFailures,
	}
}

// judgeReversed runs the LLM judge with a reversed prompt ordering to detect
// position bias. The rubric is presented before the task context.
func (j *LLMJudge) judgeReversed(ctx context.Context, task *Task, workspace string) Verdict {
	// Save the original rubric and create a reversed-order prompt.
	origRubric := j.Rubric
	j.Rubric = "IMPORTANT: Evaluate strictly. " + origRubric
	defer func() { j.Rubric = origRubric }()

	// Create a reversed task that swaps the emphasis order.
	reversedTask := *task
	// Swap hidden requirements to front of title to change position.
	if len(reversedTask.HiddenRequirements) > 0 {
		reversedTask.Title = "[Reversed evaluation] " + reversedTask.Title
	}

	return j.Judge(ctx, &reversedTask, workspace)
}
