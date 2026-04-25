// Stand-alone comparison tool: run the deterministic critique on a
// saved SOW and print the findings. Compare manually against what
// the LLM critique said.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/RelayOne/r1-agent/internal/plan"
)

type raw struct {
	GeneratedSOW plan.SOW `json:"generated_sow"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: critique_compare <sow.json>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Try the "generated_sow" wrapper first; fall back to bare SOW.
	var r raw
	var sow *plan.SOW
	if err := json.Unmarshal(data, &r); err == nil && len(r.GeneratedSOW.Sessions) > 0 {
		sow = &r.GeneratedSOW
	} else {
		var s plan.SOW
		if err := json.Unmarshal(data, &s); err != nil {
			fmt.Fprintln(os.Stderr, "parse:", err)
			os.Exit(1)
		}
		sow = &s
	}
	c := plan.CritiqueDeterministic(sow)
	fmt.Print(plan.FormatCritique(c))
}
