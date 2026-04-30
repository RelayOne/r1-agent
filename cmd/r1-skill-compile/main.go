package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/RelayOne/r1/internal/r1skill/analyze"
	"github.com/RelayOne/r1/internal/r1skill/ir"
)

func main() {
	checkOnly := flag.Bool("check", false, "validate and compile without writing proof output")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: r1-skill-compile [--check] <skill.r1.json>")
		os.Exit(2)
	}
	path := flag.Arg(0)
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read skill: %v\n", err)
		os.Exit(1)
	}
	var skill ir.Skill
	if err := json.Unmarshal(data, &skill); err != nil {
		fmt.Fprintf(os.Stderr, "parse skill: %v\n", err)
		os.Exit(1)
	}
	proof, err := analyze.Analyze(&skill, analyze.Constitution{Hash: "sha256:local-dev"}, analyze.DefaultOptions())
	if err != nil {
		fmt.Fprintf(os.Stderr, "analyze skill: %v\n", err)
		os.Exit(1)
	}
	if *checkOnly {
		fmt.Printf("OK %s %s\n", skill.SkillID, proof.IRHash)
		return
	}
	outPath := filepath.Join(filepath.Dir(path), trimExt(filepath.Base(path))+".proof.json")
	proofData, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal proof: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPath, proofData, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write proof: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("WROTE %s\n", outPath)
}

func trimExt(name string) string {
	ext := filepath.Ext(name)
	return name[:len(name)-len(ext)]
}
