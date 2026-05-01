package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/r1skill/analyze"
	"github.com/RelayOne/r1/internal/r1skill/registry"
	"github.com/RelayOne/r1/internal/r1skill/wizard"
	"github.com/RelayOne/r1/internal/r1skill/wizard/ledgerlink"
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
	case "register":
		runWizardRegisterCmd(args[1:])
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
	ledgerDir := fs.String("ledger-dir", "", "persist session evidence to this ledger directory")
	missionID := fs.String("mission-id", "", "ledger mission id for persisted session evidence")
	createdBy := fs.String("created-by", "", "ledger created_by value for persisted session evidence")
	fs.Parse(args)

	result, err := wizard.Run(context.Background(), wizard.RunOptions{
		SkillID:      *skillID,
		Mode:         *mode,
		OperatorID:   *operator,
		SourcePath:   *source,
		SourceFormat: *format,
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
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

	if *ledgerDir != "" {
		sessionID, persistErr := persistWizardSession(*ledgerDir, *missionID, nonEmptyString(*createdBy, nonEmptyString(*operator, "r1 wizard")), result, proof)
		if persistErr != nil {
			fmt.Fprintf(os.Stderr, "wizard ledger persist: %v\n", persistErr)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stdout, "ledger_session: %s\n", sessionID)
	}
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
	ledgerDir := fs.String("ledger-dir", "", "ledger directory to query instead of a decisions file")
	sessionID := fs.String("session-id", "", "exact skill_authoring_decisions node id when querying a ledger")
	questionID := fs.String("question-id", "", "exact question id")
	prefix := fs.String("question-prefix", "", "question id prefix")
	mode := fs.String("mode", "", "operator|llm-best-judgment")
	pathPrefix := fs.String("ir-path-prefix", "", "IR path prefix")
	fs.Parse(args)
	log, err := loadDecisionLog(*ledgerDir, *sessionID, *file)
	if err != nil {
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

func runWizardRegisterCmd(args []string) {
	fs := flag.NewFlagSet("wizard register", flag.ExitOnError)
	skillPath := fs.String("skill", "", "skill.r1.json file to register")
	proofPath := fs.String("proof", "", "compile proof json file")
	root := fs.String("root", "skills", "registry root directory")
	fs.Parse(args)
	if *skillPath == "" || *proofPath == "" {
		fmt.Fprintln(os.Stderr, "wizard register: --skill and --proof are required")
		os.Exit(1)
	}
	skill, err := registry.LoadSkill(*skillPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wizard register: %v\n", err)
		os.Exit(1)
	}
	proof, err := registry.LoadProof(*proofPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wizard register: %v\n", err)
		os.Exit(1)
	}
	entry, err := registry.SaveEntry(*root, skill, proof)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wizard register: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "registered_skill: %s\nregistered_proof: %s\n", entry.SourcePath, entry.ProofPath)
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

func persistWizardSession(ledgerDir, missionID, createdBy string, result *wizard.RunResult, proof *analyze.CompileProof) (ledger.NodeID, error) {
	lg, err := ledger.New(ledgerDir)
	if err != nil {
		return "", err
	}
	defer lg.Close()
	artifactRoot := filepath.Join(filepath.Dir(ledgerDir), "artifacts")
	writer, err := ledgerlink.NewWriter(lg, artifactRoot)
	if err != nil {
		return "", err
	}
	persisted, err := writer.Persist(context.Background(), result, proof, ledgerlink.PersistOptions{
		MissionID:  missionID,
		CreatedBy:  createdBy,
		StanceID:   createdBy,
		SourcePath: result.SourcePath,
	})
	if err != nil {
		return "", err
	}
	return persisted.SessionNodeID, nil
}

func loadDecisionLog(ledgerDir, sessionID, file string) (*wizard.SkillAuthoringDecisions, error) {
	if ledgerDir != "" {
		if sessionID == "" {
			return nil, fmt.Errorf("--session-id is required with --ledger-dir")
		}
		lg, err := ledger.New(ledgerDir)
		if err != nil {
			return nil, err
		}
		defer lg.Close()
		node, err := lg.Get(context.Background(), sessionID)
		if err != nil {
			return nil, err
		}
		var log wizard.SkillAuthoringDecisions
		if err := json.Unmarshal(node.Content, &log); err != nil {
			return nil, err
		}
		return &log, nil
	}
	if file == "" {
		return nil, fmt.Errorf("--decisions is required when --ledger-dir is unset")
	}
	var log wizard.SkillAuthoringDecisions
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &log); err != nil {
		return nil, err
	}
	return &log, nil
}

func nonEmptyString(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
