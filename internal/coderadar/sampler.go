// Package coderadar — per-event sampling.
//
// CODERADAR_SAMPLE_RATE controls the default rate per env. Hot events
// (provider.call_completed, tool.call_completed) get a separate
// per-event override so prod stays under its cost ceiling without
// silencing low-volume governance events.
package coderadar

import (
	"math/rand"
	"os"
	"strconv"
	"sync"
)

// Sampler decides whether an event should be emitted based on the
// configured per-event rate and the default rate. Safe for concurrent
// use after construction.
type Sampler struct {
	defaultRate float64
	perEvent    map[string]float64

	mu  sync.Mutex
	rng *rand.Rand
}

// HighVolumeEvents are the events that get the prod 10% sample rate.
// Everything else stays at 100% in every environment so governance
// signal is never dropped.
var HighVolumeEvents = map[string]bool{
	EventProviderCallCompleted: true,
	EventToolCallCompleted:     true,
}

// NewSampler constructs a Sampler. defaultRate is clamped to [0, 1].
// perEvent overrides default per name; nil means no overrides.
func NewSampler(defaultRate float64, perEvent map[string]float64) *Sampler {
	if defaultRate < 0 {
		defaultRate = 0
	}
	if defaultRate > 1 {
		defaultRate = 1
	}
	clamped := make(map[string]float64, len(perEvent))
	for k, v := range perEvent {
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		clamped[k] = v
	}
	// Tests need a deterministic seed when set via env.
	var src rand.Source
	if seed := os.Getenv("CODERADAR_SAMPLE_SEED"); seed != "" {
		if n, err := strconv.ParseInt(seed, 10, 64); err == nil {
			src = rand.NewSource(n)
		}
	}
	if src == nil {
		src = rand.NewSource(rand.Int63())
	}
	return &Sampler{
		defaultRate: defaultRate,
		perEvent:    clamped,
		rng:         rand.New(src),
	}
}

// SamplerForEnv returns the Sampler configured per environment. Reads
// CODERADAR_SAMPLE_RATE for the default; falls back to env defaults if
// unset. Prod additionally pins high-volume events to 10%.
func SamplerForEnv(env string) *Sampler {
	def, ok := readSampleRateEnv()
	if !ok {
		switch env {
		case "prod", "production":
			def = 1.0 // default for low-volume events
		case "staging":
			def = 1.0
		case "dev", "development":
			def = 1.0
		case "local":
			def = 0.0
		default:
			def = 1.0
		}
	}
	var perEvent map[string]float64
	if env == "prod" || env == "production" {
		perEvent = map[string]float64{
			EventProviderCallCompleted: 0.1,
			EventToolCallCompleted:     0.1,
		}
	}
	return NewSampler(def, perEvent)
}

// readSampleRateEnv parses CODERADAR_SAMPLE_RATE if present.
func readSampleRateEnv() (float64, bool) {
	raw := os.Getenv("CODERADAR_SAMPLE_RATE")
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return v, true
}

// ShouldEmit returns true if the event should be forwarded.
func (s *Sampler) ShouldEmit(eventName string) bool {
	if s == nil {
		return true
	}
	rate := s.defaultRate
	if s.perEvent != nil {
		if r, ok := s.perEvent[eventName]; ok {
			rate = r
		}
	}
	if rate >= 1.0 {
		return true
	}
	if rate <= 0.0 {
		return false
	}
	s.mu.Lock()
	v := s.rng.Float64()
	s.mu.Unlock()
	return v < rate
}

// RateFor returns the configured rate for an event name. Useful for
// tests and the runbook stats command.
func (s *Sampler) RateFor(eventName string) float64 {
	if s == nil {
		return 1.0
	}
	if s.perEvent != nil {
		if r, ok := s.perEvent[eventName]; ok {
			return r
		}
	}
	return s.defaultRate
}
