package adapter

import (
	"context"
	"errors"
	"fmt"
)

type SourceArtifact struct {
	Format     string      `json:"format"`
	Path       string      `json:"path,omitempty"`
	RawBytes   []byte      `json:"raw_bytes"`
	Parsed     any         `json:"parsed,omitempty"`
	Inferences []Inference `json:"inferences,omitempty"`
}

type Inference struct {
	IRPath     string  `json:"ir_path"`
	Value      any     `json:"value"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source,omitempty"`
}

type Adapter interface {
	Format() string
	CanParse(raw []byte) error
	Parse(ctx context.Context, raw []byte, sourcePath string) (*SourceArtifact, error)
}

type Registry struct {
	byFormat map[string]Adapter
	all      []Adapter
}

func NewRegistry() *Registry {
	return &Registry{byFormat: map[string]Adapter{}}
}

func (r *Registry) Register(a Adapter) {
	r.byFormat[a.Format()] = a
	r.all = r.all[:0]
	for _, ad := range r.byFormat {
		r.all = append(r.all, ad)
	}
}

func (r *Registry) Get(format string) (Adapter, error) {
	a, ok := r.byFormat[format]
	if !ok {
		return nil, fmt.Errorf("adapter: no adapter registered for format %q", format)
	}
	return a, nil
}

func (r *Registry) Detect(raw []byte) (Adapter, error) {
	var matches []Adapter
	for _, a := range r.all {
		if err := a.CanParse(raw); err == nil {
			matches = append(matches, a)
		}
	}
	if len(matches) == 0 {
		return nil, errors.New("adapter: no adapter could parse source")
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("adapter: source is ambiguous; specify --source-format")
	}
	return matches[0], nil
}

func (s *SourceArtifact) Validate() error {
	if s.Format == "" {
		return errors.New("source artifact: format required")
	}
	if len(s.RawBytes) == 0 {
		return errors.New("source artifact: raw_bytes required")
	}
	for i, inf := range s.Inferences {
		if inf.IRPath == "" {
			return fmt.Errorf("source artifact: inference %d missing ir_path", i)
		}
		if inf.Confidence < 0 || inf.Confidence > 1 {
			return fmt.Errorf("source artifact: inference %d confidence out of range", i)
		}
	}
	return nil
}

var Default = NewRegistry()
