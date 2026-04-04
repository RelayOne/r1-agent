package tfidf

import (
	"testing"
)

func TestTokenize(t *testing.T) {
	tokens := tokenize("camelCaseFunction snake_case_var")
	found := make(map[string]bool)
	for _, tok := range tokens {
		found[tok] = true
	}

	if !found["camel"] || !found["case"] || !found["function"] {
		t.Errorf("should split camelCase: %v", tokens)
	}
	if !found["snake"] {
		t.Errorf("should split snake_case and include snake: %v", tokens)
	}
}

func TestTokenizeStopWords(t *testing.T) {
	tokens := tokenize("the function is at the end")
	for _, tok := range tokens {
		if tok == "the" || tok == "is" || tok == "at" {
			t.Errorf("stop word should be filtered: %s", tok)
		}
	}
}

func TestComputeTF(t *testing.T) {
	terms := []string{"hello", "world", "hello"}
	tf := computeTF(terms)
	if tf["hello"] < 0.6 || tf["hello"] > 0.7 {
		t.Errorf("hello TF should be ~0.66, got %f", tf["hello"])
	}
	if tf["world"] < 0.3 || tf["world"] > 0.4 {
		t.Errorf("world TF should be ~0.33, got %f", tf["world"])
	}
}

func TestIndexAndSearch(t *testing.T) {
	idx := NewIndex()

	idx.AddDocument("auth.go", `package auth
func Login(username, password string) error {
	return validateCredentials(username, password)
}
func Logout(sessionID string) {
	invalidateSession(sessionID)
}`)

	idx.AddDocument("server.go", `package server
func StartHTTP(port int) error {
	return http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
}`)

	idx.AddDocument("db.go", `package database
func Connect(connectionString string) (*DB, error) {
	return sql.Open("postgres", connectionString)
}`)

	idx.Finalize()

	results := idx.Search("authentication login credentials", 5)
	if len(results) == 0 {
		t.Fatal("should find results")
	}
	if results[0].Path != "auth.go" {
		t.Errorf("auth.go should rank first, got %s", results[0].Path)
	}
}

func TestSearchNoResults(t *testing.T) {
	idx := NewIndex()
	idx.AddDocument("a.go", "package main")
	idx.Finalize()

	results := idx.Search("zzzznonexistent", 5)
	if len(results) != 0 {
		t.Error("should have no results for nonexistent term")
	}
}

func TestSearchWithContext(t *testing.T) {
	idx := NewIndex()
	idx.AddDocument("auth.go", "func Login(username string) error { return nil }")
	idx.AddDocument("db.go", "func Connect(host string) error { return nil }")
	idx.Finalize()

	results := idx.SearchWithContext("login username", 5)
	if len(results) == 0 {
		t.Fatal("should find results")
	}
	if len(results[0].MatchingTerms) == 0 {
		t.Error("should have matching terms")
	}
}

func TestDocCount(t *testing.T) {
	idx := NewIndex()
	idx.AddDocument("a.go", "package main")
	idx.AddDocument("b.go", "package util")

	if idx.DocCount() != 2 {
		t.Errorf("expected 2 docs, got %d", idx.DocCount())
	}
}

func TestTermCount(t *testing.T) {
	idx := NewIndex()
	idx.AddDocument("a.go", "func hello world")
	idx.Finalize()

	if idx.TermCount() == 0 {
		t.Error("should have some terms")
	}
}

func TestTopK(t *testing.T) {
	idx := NewIndex()
	for i := 0; i < 10; i++ {
		idx.AddDocument("file.go", "func search query match result")
	}
	idx.Finalize()

	results := idx.Search("search query", 3)
	if len(results) > 3 {
		t.Errorf("topK should limit to 3, got %d", len(results))
	}
}

func TestEmptyIndex(t *testing.T) {
	idx := NewIndex()
	idx.Finalize()

	results := idx.Search("anything", 5)
	if len(results) != 0 {
		t.Error("empty index should return no results")
	}
}

func TestExpandIdentifiers(t *testing.T) {
	result := expandIdentifiers("getUserName parseHTTPResponse")
	if result == "getUserName parseHTTPResponse" {
		t.Error("should split camelCase")
	}
}

func TestShouldSkip(t *testing.T) {
	if !shouldSkip("vendor/pkg/x.go") {
		t.Error("should skip vendor")
	}
	if shouldSkip("internal/pkg/x.go") {
		t.Error("should not skip internal")
	}
}

func TestIDFWeighting(t *testing.T) {
	idx := NewIndex()
	// "common" appears in all docs, "rare" in only one
	idx.AddDocument("a.go", "common function common common")
	idx.AddDocument("b.go", "common function common common")
	idx.AddDocument("c.go", "common rare special unique")
	idx.Finalize()

	// Search for rare term should rank c.go highest
	results := idx.Search("rare", 5)
	if len(results) == 0 {
		t.Fatal("should find results")
	}
	if results[0].Path != "c.go" {
		t.Errorf("c.go should rank first for rare term, got %s", results[0].Path)
	}
}
