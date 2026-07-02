package main

import (
	"fmt"
	"path/filepath"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/wisdom"
)

// newPersistentWisdom returns a cross-run persistent wisdom store rooted at
// <repo>/.stoke/wisdom.db so a fix learned for a failure fingerprint in one
// run is recalled (via FindByPattern) in later runs — the difference between
// a "learning loop" as a slogan and later runs actually getting faster
// (proposal P4). Falls back to the in-memory store when SQLite is
// unavailable (e.g. a CGO-less stub build), so behavior never regresses.
func newPersistentWisdom(repo string) wisdom.Recorder {
	if repo == "" {
		return wisdom.NewStore()
	}
	path := filepath.Join(r1dir.JoinFor(repo), "wisdom.db")
	s, err := wisdom.NewSQLiteStore(path)
	if err != nil {
		fmt.Printf("  ⚠ wisdom: persistent store unavailable (%v) — using in-memory (learnings won't survive restart)\n", err)
		return wisdom.NewStore()
	}
	return s
}
