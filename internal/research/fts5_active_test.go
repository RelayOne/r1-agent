//go:build sqlite_fts5

package research

import "testing"

// TestFTS5ActiveUnderBuildTag is a regression guard for the dormant-FTS5 bug:
// the shipped binary is built with `-tags sqlite_fts5` (see Makefile / install.sh),
// and under that tag the vendored sqlite3 must actually provide FTS5 so that
// Search() uses the MATCH path rather than silently degrading to the LIKE
// fallback. If this test ever fails, FTS5 has gone dormant again — most likely
// the build tag was dropped from the sqlite3 driver, not from this package.
func TestFTS5ActiveUnderBuildTag(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()
	if !s.hasFTS {
		t.Fatal("built with -tags sqlite_fts5 but FTS5 virtual table creation failed; " +
			"research.Search is running on the LIKE fallback (FTS5 is dormant)")
	}
}
