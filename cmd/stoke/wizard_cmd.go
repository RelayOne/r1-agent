package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/r1skill/analyze"
	"github.com/RelayOne/r1/internal/r1skill/wizard"
)

func skillWizardCmd(args []string) {
	if len(args) == 0 {
		runWizardAuthorCmd(nil)
		return
	}
	switch args[0] {
	case "run":
		runWizardAuthorCmd(args[1:])
	case "migrate":
		runWizardMigrateCmd(args[1:])
	case "query":
		runWizardQueryCmd(args[1:])
	default:
		runWizardAuthorCmd(args)
	}
}

func runWizardAuthorCmd(args []string) {
	fs := flag.NewFlagSet("wizard run", flag.ExitOnError)
	source := fs.String("from", "", "source file to convert")
	format := fs.String("source-format", "", "source format override")
	mode := fs.String("mode", "interactive", "interactive|headless|hybrid")
	skillID := fs.String("skill-id", "", "explicit skill id")
	outDir := fs.String("out-dir", ".", "output directory")
	operator := fs.String("operator", "", "operator id")
	fs.Parse(args)

	result, err := wizard.Run(context.Background(), wizard.RunOptions{
		SkillID:      *skillID,
		Mode:         *mode,
		OperatorID:   *operator,
		SourcePath:   *source,
		SourceFormat: *format,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "wizard error: %v\n", err)
		os.Exit(1)
	}
	proof, err := analyze.Analyze(result.Skill, analyze.Constitution{Hash: "wizard-cli"}, analyze.DefaultOptions())
	if err != nil {
		fmt.Fprintf(os.Stderr, "wizard analyze error: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "wizard mkdir: %v\n", err)
		os.Exit(1)
	}
	skillPath := filepath.Join(*outDir, result.Skill.SkillID+".r1.json")
	proofPath := filepath.Join(*outDir, result.Skill.SkillID+".proof.json")
	decisionsPath := filepath.Join(*outDir, result.Skill.SkillID+".decisions.json")
	writeJSON(skillPath, result.Skill)
	writeJSON(proofPath, proof)
	writeJSON(decisionsPath, result.Decisions)
	fmt.Fprintf(os.Stdout, "skill: %s\nproof: %s\ndecisions: %s\n", skillPath, proofPath, decisionsPath)
}

func runWizardMigrateCmd(args []string) {
	fs := flag.NewFlagSet("wizard migrate", flag.ExitOnError)
	sourceDir := fs.String("source-dir", "", "source directory")
	format := fs.String("source-format", "", "source format")
	outDir := fs.String("output-dir", ".", "output directory")
	mode := fs.String("mode", "headless", "interactive|headless|hybrid")
	fs.Parse(args)
	if *sourceDir == "" {
		fmt.Fprintln(os.Stderr, "wizard migrate: --source-dir is required")
		os.Exit(1)
	}
	entries, err := os.ReadDir(*sourceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wizard migrate: %v\n", err)
		os.Exit(1)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		runWizardAuthorCmd([]string{
			"--from", filepath.Join(*sourceDir, entry.Name()),
			"--source-format", *format,
			"--mode", *mode,
			"--out-dir", *outDir,
		})
	}
}

func runWizardQueryCmd(args []string) {
	fs := flag.NewFlagSet("wizard query", flag.ExitOnError)
	file := fs.String("decisions", "", "decision log json file")
	questionID := fs.String("question-id", "", "exact question id")
	prefix := fs.String("question-prefix", "", "question id prefix")
	mode := fs.String("mode", "", "operator|llm-best-judgment")
	pathPrefix := fs.String("ir-path-prefix", "", "IR path prefix")
	fs.Parse(args)
	if *file == "" {
		fmt.Fprintln(os.Stderr, "wizard query: --decisions is required")
		os.Exit(1)
	}
	var log wizard.SkillAuthoringDecisions
	data, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wizard query: %v\n", err)
		os.Exit(1)
	}
	if err := json.Unmarshal(data, &log); err != nil {
		fmt.Fprintf(os.Stderr, "wizard query: %v\n", err)
		os.Exit(1)
	}
	matches := log.Filter(wizard.Query{
		QuestionID:       strings.TrimSpace(*questionID),
		QuestionIDPrefix: strings.TrimSpace(*prefix),
		Mode:             strings.TrimSpace(*mode),
		IRPathPrefix:     strings.TrimSpace(*pathPrefix),
	})
	writeJSONTo(os.Stdout, matches)
}

func writeJSON(path string, v any) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wizard write: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()
	writeJSONTo(f, v)
}

func writeJSONTo(f *os.File, v any) {
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "wizard encode: %v\n", err)
		os.Exit(1)
	}
}
