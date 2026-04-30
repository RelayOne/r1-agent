package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RelayOne/r1/internal/r1skill/analyze"
	"github.com/RelayOne/r1/internal/r1skill/ir"
)

type Entry struct {
	Skill      *ir.Skill
	Proof      *analyze.CompileProof
	SourcePath string
	ProofPath  string
}

type Registry struct {
	entries map[string]Entry
}

func New() *Registry {
	return &Registry{entries: map[string]Entry{}}
}

func (r *Registry) LoadDir(root string) error {
	if root == "" {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !isSkillFile(path) {
			return nil
		}
		entry, loadErr := LoadEntry(path)
		if loadErr != nil {
			return loadErr
		}
		r.entries[entry.Skill.SkillID] = entry
		return nil
	})
}

func (r *Registry) Get(id string) (Entry, bool) {
	entry, ok := r.entries[id]
	return entry, ok
}

func (r *Registry) List() []Entry {
	out := make([]Entry, 0, len(r.entries))
	for _, entry := range r.entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Skill.SkillID < out[j].Skill.SkillID
	})
	return out
}

func LoadEntry(path string) (Entry, error) {
	skill, err := LoadSkill(path)
	if err != nil {
		return Entry{}, err
	}
	proofPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".proof.json"
	proof, err := LoadProof(proofPath)
	if err != nil {
		return Entry{}, err
	}
	return Entry{
		Skill:      skill,
		Proof:      proof,
		SourcePath: path,
		ProofPath:  proofPath,
	}, nil
}

func LoadSkill(path string) (*ir.Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("r1skill/registry: read skill %s: %w", path, err)
	}
	var skill ir.Skill
	if err := json.Unmarshal(data, &skill); err != nil {
		return nil, fmt.Errorf("r1skill/registry: parse %s as canonical JSON IR: %w", path, err)
	}
	if err := skill.Validate(); err != nil {
		return nil, err
	}
	return &skill, nil
}

func LoadProof(path string) (*analyze.CompileProof, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("r1skill/registry: read proof %s: %w", path, err)
	}
	var proof analyze.CompileProof
	if err := json.Unmarshal(data, &proof); err != nil {
		return nil, fmt.Errorf("r1skill/registry: parse proof %s: %w", path, err)
	}
	if proof.IRHash == "" {
		return nil, fmt.Errorf("r1skill/registry: proof %s missing ir_hash", path)
	}
	return &proof, nil
}

func SaveEntry(root string, skill *ir.Skill, proof *analyze.CompileProof) (Entry, error) {
	if skill == nil {
		return Entry{}, fmt.Errorf("r1skill/registry: skill is required")
	}
	if proof == nil {
		return Entry{}, fmt.Errorf("r1skill/registry: proof is required")
	}
	if err := skill.Validate(); err != nil {
		return Entry{}, err
	}
	if proof.IRHash == "" {
		return Entry{}, fmt.Errorf("r1skill/registry: proof missing ir_hash")
	}
	dir := filepath.Join(root, skill.SkillID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Entry{}, fmt.Errorf("r1skill/registry: mkdir %s: %w", dir, err)
	}
	skillPath := filepath.Join(dir, "skill.r1.json")
	proofPath := filepath.Join(dir, "skill.r1.proof.json")
	skillBytes, err := json.MarshalIndent(skill, "", "  ")
	if err != nil {
		return Entry{}, fmt.Errorf("r1skill/registry: encode %s: %w", skillPath, err)
	}
	if err := writeJSONFile(skillPath, skillBytes); err != nil {
		return Entry{}, err
	}
	proofBytes, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		return Entry{}, fmt.Errorf("r1skill/registry: encode %s: %w", proofPath, err)
	}
	if err := writeJSONFile(proofPath, proofBytes); err != nil {
		return Entry{}, err
	}
	return Entry{
		Skill:      skill,
		Proof:      proof,
		SourcePath: skillPath,
		ProofPath:  proofPath,
	}, nil
}

func writeJSONFile(path string, payload []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("r1skill/registry: create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("r1skill/registry: write %s: %w", path, err)
	}
	return nil
}

func isSkillFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, ".proof.json") {
		return false
	}
	return strings.HasSuffix(base, ".r1.json")
}
