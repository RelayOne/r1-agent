package wizard

import (
	"bufio"
	"fmt"
	"strings"
)

// presentProposal shows the auto-generated config and asks for approval.
// Uses stdin/stdout for interaction (no huh dependency).
func presentProposal(opts Opts, r *WizardResult) error {
	// Build and display proposal
	proposal := renderProposal(r)
	fmt.Fprint(opts.Stdout, proposal)

	fmt.Fprintf(opts.Stdout, "\n  [1] Accept all\n  [2] Cancel\n  [1] > ")

	if opts.Stdin == nil {
		return nil // no stdin available, accept defaults
	}

	scanner := bufio.NewScanner(opts.Stdin)
	if !scanner.Scan() {
		return nil // EOF, accept defaults
	}
	input := strings.TrimSpace(scanner.Text())

	switch input {
	case "", "1":
		return nil
	case "2":
		return fmt.Errorf("wizard cancelled by user")
	default:
		return nil // unknown input, accept defaults
	}
}
