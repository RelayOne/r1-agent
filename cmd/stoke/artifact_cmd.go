package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1/internal/artifact"
	"github.com/RelayOne/r1/internal/artifact/antigravity"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
	"github.com/RelayOne/r1/internal/r1dir"
)

func artifactCmd(args []string) {
	if len(args) == 0 {
		fatal("usage: stoke artifact <list|show|annotate|accept|reject|export-antigravity|import-antigravity> ...")
	}
	switch args[0] {
	case "list":
		artifactListCmd(args[1:])
	case "show":
		artifactShowCmd(args[1:])
	case "annotate":
		artifactAnnotateCmd(args[1:])
	case "accept":
		artifactQuickAnnotateCmd(args[1:], "accept")
	case "reject":
		artifactQuickAnnotateCmd(args[1:], "reject")
	case "export-antigravity":
		artifactExportCmd(args[1:])
	case "import-antigravity":
		artifactImportCmd(args[1:])
	default:
		fatal("unknown artifact verb %q", args[0])
	}
}

func openArtifactStores(repo string) (*ledger.Ledger, *artifact.Store, error) {
	ledgerDir := r1dir.JoinFor(repo, "ledger")
	lg, err := ledger.New(ledgerDir)
	if err != nil {
		return nil, nil, err
	}
	store, err := artifact.NewStore(filepath.Join(filepath.Dir(ledgerDir), "artifacts"))
	if err != nil {
		lg.Close()
		return nil, nil, err
	}
	return lg, store, nil
}

func artifactListCmd(args []string) {
	fs := flag.NewFlagSet("artifact list", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	missionID := fs.String("mission", "", "Mission ID")
	fs.Parse(args)
	if *missionID == "" {
		fatal("artifact list: --mission is required")
	}
	absRepo, _ := filepath.Abs(*repo)
	lg, _, err := openArtifactStores(absRepo)
	if err != nil {
		fatal("artifact list: %v", err)
	}
	defer lg.Close()
	ids, err := lg.QueryNodes(ledger.QueryFilter{Type: "artifact", MissionID: *missionID})
	if err != nil {
		fatal("artifact list: %v", err)
	}
	for _, id := range ids {
		node, err := lg.ReadNode(id)
		if err != nil {
			continue
		}
		var art nodes.Artifact
		if err := json.Unmarshal(node.Content, &art); err != nil {
			continue
		}
		fmt.Printf("%s\t%s\t%s\n", id, art.ArtifactKind, art.Title)
	}
}

func artifactShowCmd(args []string) {
	fs := flag.NewFlagSet("artifact show", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	raw := fs.Bool("raw", false, "Only print content bytes")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fatal("usage: stoke artifact show [--repo PATH] [--raw] <artifact-id>")
	}
	absRepo, _ := filepath.Abs(*repo)
	lg, store, err := openArtifactStores(absRepo)
	if err != nil {
		fatal("artifact show: %v", err)
	}
	defer lg.Close()
	node, err := lg.ReadNode(ledger.NodeID(fs.Arg(0)))
	if err != nil {
		fatal("artifact show: %v", err)
	}
	var art nodes.Artifact
	if err := json.Unmarshal(node.Content, &art); err != nil {
		fatal("artifact show: %v", err)
	}
	content, err := store.Get(art.ContentRef)
	if err != nil {
		fatal("artifact show: %v", err)
	}
	if !*raw {
		fmt.Printf("kind=%s title=%s content_type=%s size=%d\n", art.ArtifactKind, art.Title, art.ContentType, art.SizeBytes)
	}
	_, _ = os.Stdout.Write(content)
	if len(content) == 0 || content[len(content)-1] != '\n' {
		fmt.Println()
	}
}

func artifactAnnotateCmd(args []string) {
	fs := flag.NewFlagSet("artifact annotate", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	action := fs.String("action", "comment", "comment|reject|accept|amend")
	body := fs.String("body", "", "Annotation body")
	amendmentRef := fs.String("amendment-ref", "", "Replacement artifact ref for amend")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fatal("usage: stoke artifact annotate [flags] <artifact-id>")
	}
	absRepo, _ := filepath.Abs(*repo)
	lg, _, err := openArtifactStores(absRepo)
	if err != nil {
		fatal("artifact annotate: %v", err)
	}
	defer lg.Close()
	parent, err := lg.ReadNode(ledger.NodeID(fs.Arg(0)))
	if err != nil {
		fatal("artifact annotate: %v", err)
	}
	ann := nodes.ArtifactAnnotation{
		ArtifactRef:   fs.Arg(0),
		AnnotatorID:   currentOperator(),
		AnnotatorRole: "operator",
		Action:        *action,
		Body:          *body,
		AmendmentRef:  *amendmentRef,
		When:          time.Now().UTC(),
		Version:       1,
	}
	bodyBytes, err := json.Marshal(&ann)
	if err != nil {
		fatal("artifact annotate: %v", err)
	}
	id, err := lg.AddNode(context.Background(), ledger.Node{
		Type:          "artifact_annotation",
		SchemaVersion: 1,
		CreatedBy:     ann.AnnotatorID,
		MissionID:     parent.MissionID,
		Content:       bodyBytes,
	})
	if err != nil {
		fatal("artifact annotate: %v", err)
	}
	if err := lg.AddEdge(context.Background(), ledger.Edge{From: id, To: fs.Arg(0), Type: ledger.EdgeReferences}); err != nil {
		fatal("artifact annotate: %v", err)
	}
	fmt.Println(id)
}

func artifactQuickAnnotateCmd(args []string, action string) {
	fs := flag.NewFlagSet("artifact "+action, flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	body := fs.String("body", "", "Annotation body")
	fs.Parse(args)
	rest := []string{"--repo", *repo, "--action", action, "--body", *body}
	if fs.NArg() == 1 {
		rest = append(rest, fs.Arg(0))
	}
	artifactAnnotateCmd(rest)
}

func artifactExportCmd(args []string) {
	fs := flag.NewFlagSet("artifact export-antigravity", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	missionID := fs.String("mission", "", "Mission ID")
	out := fs.String("out", "", "Output path")
	fs.Parse(args)
	if *missionID == "" {
		fatal("artifact export-antigravity: --mission is required")
	}
	absRepo, _ := filepath.Abs(*repo)
	lg, store, err := openArtifactStores(absRepo)
	if err != nil {
		fatal("artifact export-antigravity: %v", err)
	}
	defer lg.Close()
	bundle, err := buildAntigravityBundle(lg, store, *missionID)
	if err != nil {
		fatal("artifact export-antigravity: %v", err)
	}
	wire, err := antigravity.ToAntigravity(bundle)
	if err != nil {
		fatal("artifact export-antigravity: %v", err)
	}
	data, _ := json.MarshalIndent(wire, "", "  ")
	if *out == "" {
		fmt.Println(string(data))
		return
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fatal("artifact export-antigravity: %v", err)
	}
}

func artifactImportCmd(args []string) {
	fs := flag.NewFlagSet("artifact import-antigravity", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fatal("usage: stoke artifact import-antigravity [--repo PATH] <bundle.json>")
	}
	absRepo, _ := filepath.Abs(*repo)
	lg, store, err := openArtifactStores(absRepo)
	if err != nil {
		fatal("artifact import-antigravity: %v", err)
	}
	defer lg.Close()
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fatal("artifact import-antigravity: %v", err)
	}
	var wire antigravity.Bundle
	if err := json.Unmarshal(data, &wire); err != nil {
		fatal("artifact import-antigravity: %v", err)
	}
	r1Bundle, err := antigravity.FromAntigravity(wire)
	if err != nil {
		fatal("artifact import-antigravity: %v", err)
	}
	builder := artifact.NewBuilder(store, lg)
	for _, item := range r1Bundle.Items {
		id, err := builder.Emit(context.Background(), artifact.EmitParams{
			Kind:              item.Artifact.ArtifactKind,
			Title:             item.Artifact.Title,
			Content:           item.Bytes,
			ContentType:       item.Artifact.ContentType,
			MissionID:         r1Bundle.MissionID,
			StanceID:          item.Artifact.StanceID,
			AntigravitySource: item.Artifact.AntigravitySource,
			When:              item.Artifact.When,
		})
		if err != nil {
			fatal("artifact import-antigravity: %v", err)
		}
		for _, ann := range item.Annotations {
			ann.ArtifactRef = string(id)
			if _, err := builder.EmitAnnotation(context.Background(), r1Bundle.MissionID, ann); err != nil {
				fatal("artifact import-antigravity: %v", err)
			}
		}
	}
}

func buildAntigravityBundle(lg *ledger.Ledger, store *artifact.Store, missionID string) (antigravity.R1Bundle, error) {
	out := antigravity.R1Bundle{MissionID: missionID, CreatedAt: time.Now().UTC()}
	ids, err := lg.QueryNodes(ledger.QueryFilter{Type: "artifact", MissionID: missionID})
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		node, err := lg.ReadNode(id)
		if err != nil {
			return out, err
		}
		var art nodes.Artifact
		if err := json.Unmarshal(node.Content, &art); err != nil {
			return out, err
		}
		bytes, _ := store.Get(art.ContentRef)
		out.Items = append(out.Items, antigravity.R1Item{ArtifactID: id, Artifact: art, Bytes: bytes})
	}
	return out, nil
}

func currentOperator() string {
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	return "operator"
}
