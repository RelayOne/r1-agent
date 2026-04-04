package convergence

import (
	"strings"
	"testing"
)

func TestExtractFileSymbols_Go(t *testing.T) {
	code := []byte(`package auth

func Login(user, pass string) string {
	return generateJWT(user)
}

func generateJWT(user string) string {
	return "token"
}

type AuthService struct {
	db Database
}

func (s *AuthService) Validate(token string) bool {
	return true
}
`)
	syms := extractFileSymbols("auth.go", code)
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}

	if !names["Login"] {
		t.Error("should extract Login function")
	}
	if !names["generateJWT"] {
		t.Error("should extract generateJWT function")
	}
	if !names["AuthService"] {
		t.Error("should extract AuthService type")
	}
	if !names["Validate"] {
		t.Error("should extract Validate method")
	}

	// Check exported detection
	for _, s := range syms {
		if s.Name == "Login" && !s.Exported {
			t.Error("Login should be exported")
		}
		if s.Name == "generateJWT" && s.Exported {
			t.Error("generateJWT should not be exported")
		}
	}
}

func TestExtractFileSymbols_TypeScript(t *testing.T) {
	code := []byte(`export function fetchUsers(): Promise<User[]> {
  return api.get('/users')
}

export class UserService {
  getUser(id: string) {}
}

export interface UserPayload {
  name: string
}

export type UserID = string
`)
	syms := extractFileSymbols("service.ts", code)
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}

	if !names["fetchUsers"] {
		t.Error("should extract fetchUsers")
	}
	if !names["UserService"] {
		t.Error("should extract UserService class")
	}
	if !names["UserPayload"] {
		t.Error("should extract UserPayload interface")
	}
	if !names["UserID"] {
		t.Error("should extract UserID type alias")
	}
}

func TestExtractFileSymbols_Python(t *testing.T) {
	code := []byte(`def process_order(order_id):
    pass

class OrderProcessor:
    def validate(self, order):
        pass
`)
	syms := extractFileSymbols("orders.py", code)
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}

	if !names["process_order"] {
		t.Error("should extract process_order")
	}
	if !names["OrderProcessor"] {
		t.Error("should extract OrderProcessor")
	}
}

func TestExtractFileSymbols_UnknownLanguage(t *testing.T) {
	syms := extractFileSymbols("readme.md", []byte("# Hello"))
	if len(syms) != 0 {
		t.Errorf("should return no symbols for unknown language, got %d", len(syms))
	}
}

func TestReferenceCount(t *testing.T) {
	files := []FileInput{
		{Path: "types.go", Content: []byte("type AuthService struct {}")},
		{Path: "main.go", Content: []byte("svc := AuthService{}\nsvc.Run()")},
		{Path: "handler.go", Content: []byte("func handle(s AuthService) {}")},
	}

	count := referenceCount("AuthService", files, "types.go")
	if count != 2 {
		t.Errorf("referenceCount = %d, want 2", count)
	}
}

func TestReferenceCountExcludesDefFile(t *testing.T) {
	files := []FileInput{
		{Path: "types.go", Content: []byte("type Foo struct{}\nvar _ = Foo{}")},
	}

	count := referenceCount("Foo", files, "types.go")
	if count != 0 {
		t.Errorf("should not count refs in definition file, got %d", count)
	}
}

func TestSemanticAnalysis_UnreachableSymbol(t *testing.T) {
	files := []FileInput{
		{Path: "feature.go", Content: []byte(`package feature

func ProcessData(input string) string {
	return transform(input)
}

func transform(s string) string {
	return s
}
`)},
	}

	findings := SemanticAnalysis(files, nil)

	// ProcessData is exported but only in one file — should be flagged
	found := false
	for _, f := range findings {
		if f.RuleID == "unreachable-symbol" && strings.Contains(f.Description, "ProcessData") {
			found = true
		}
	}
	if !found {
		t.Error("should flag exported ProcessData as unreachable when only one file exists")
	}
}

func TestSemanticAnalysis_ReachableSymbol(t *testing.T) {
	files := []FileInput{
		{Path: "feature.go", Content: []byte(`package feature

func ProcessData(input string) string {
	return input
}
`)},
		{Path: "main.go", Content: []byte(`package main

func main() {
	result := feature.ProcessData("hello")
	println(result)
}
`)},
	}

	findings := SemanticAnalysis(files, nil)

	for _, f := range findings {
		if f.RuleID == "unreachable-symbol" && strings.Contains(f.Description, "ProcessData") {
			t.Error("should NOT flag ProcessData — it's referenced in main.go")
		}
	}
}

func TestSemanticAnalysis_SkipsTestFiles(t *testing.T) {
	files := []FileInput{
		{Path: "feature_test.go", Content: []byte(`package feature

func TestSomething(t *testing.T) {
	result := ProcessData("x")
}
`)},
	}

	findings := SemanticAnalysis(files, nil)

	// Test file symbols should not be flagged
	for _, f := range findings {
		if f.RuleID == "unreachable-symbol" && strings.Contains(f.File, "_test.go") {
			t.Errorf("should not flag test file symbols: %s", f.Description)
		}
	}
}

func TestSemanticAnalysis_SkipsEntryPoints(t *testing.T) {
	files := []FileInput{
		{Path: "main.go", Content: []byte(`package main

func main() {}
func init() {}
`)},
	}

	findings := SemanticAnalysis(files, nil)

	for _, f := range findings {
		if f.RuleID == "unreachable-symbol" {
			t.Errorf("should not flag entry points: %s", f.Description)
		}
	}
}

func TestSemanticAnalysis_CriteriaMapped(t *testing.T) {
	files := []FileInput{
		{Path: "auth.go", Content: []byte(`package auth

func Login(user, pass string) string {
	token := generateJWT(user)
	return token
}
`)},
		{Path: "auth_test.go", Content: []byte(`package auth

func TestLogin(t *testing.T) {
	token := Login("user", "pass")
	if token == "" {
		t.Error("expected token")
	}
}
`)},
	}

	criteria := []string{
		"JWT tokens are issued on login",
		"Rate limiting returns 429 after threshold",
	}

	findings := SemanticAnalysis(files, criteria)

	// Rate limiting should be flagged
	rateLimitFlagged := false
	for _, f := range findings {
		if f.RuleID == "criteria-semantic" {
			desc := strings.ToLower(f.Description)
			if strings.Contains(desc, "rate") || strings.Contains(desc, "429") {
				rateLimitFlagged = true
			}
		}
	}
	if !rateLimitFlagged {
		t.Error("should flag rate limiting criterion as having weak evidence")
	}
}

func TestSemanticAnalysis_TypeWiring(t *testing.T) {
	files := []FileInput{
		{Path: "types.go", Content: []byte(`package service

type UserCache struct {
	items map[string]string
}
`)},
	}

	findings := SemanticAnalysis(files, nil)

	// UserCache is exported but never used
	found := false
	for _, f := range findings {
		if strings.Contains(f.Description, "UserCache") {
			found = true
		}
	}
	if !found {
		t.Error("should flag UserCache as unwired — defined but never instantiated")
	}
}

func TestSemanticAnalysis_TypeUsed(t *testing.T) {
	files := []FileInput{
		{Path: "types.go", Content: []byte(`package service

type UserCache struct {
	items map[string]string
}
`)},
		{Path: "handler.go", Content: []byte(`package service

func NewHandler(cache UserCache) Handler {
	return Handler{cache: cache}
}
`)},
	}

	findings := SemanticAnalysis(files, nil)

	for _, f := range findings {
		if f.RuleID == "cross-file-wiring" && strings.Contains(f.Description, "UserCache") {
			t.Error("should NOT flag UserCache — it's used in handler.go")
		}
	}
}

func TestExtractConcepts(t *testing.T) {
	concepts := extractConcepts("JWT tokens are issued on login")
	conceptSet := map[string]bool{}
	for _, c := range concepts {
		conceptSet[c] = true
	}

	if !conceptSet["jwt"] {
		t.Error("should extract 'jwt'")
	}
	if !conceptSet["tokens"] {
		t.Error("should extract 'tokens'")
	}
	if !conceptSet["issued"] {
		t.Error("should extract 'issued'")
	}
	if !conceptSet["login"] {
		t.Error("should extract 'login'")
	}

	// Should filter noise
	if conceptSet["are"] {
		t.Error("should filter 'are'")
	}
}

func TestExtractConceptsBigrams(t *testing.T) {
	concepts := extractConcepts("Rate limiting returns 429")
	hasBigram := false
	for _, c := range concepts {
		if c == "ratelimiting" {
			hasBigram = true
		}
	}
	if !hasBigram {
		t.Error("should include bigram 'ratelimiting'")
	}
}

func TestIsEntryPoint(t *testing.T) {
	tests := []struct {
		name string
		kind symbolKind
		want bool
	}{
		{"main", skFunction, true},
		{"init", skFunction, true},
		{"TestFoo", skFunction, true},
		{"BenchmarkBar", skFunction, true},
		{"HandleRequest", skFunction, true},
		{"NewController", skFunction, true},
		{"ProcessData", skFunction, false},
		{"Validate", skMethod, true}, // methods are always entry points
	}

	for _, tt := range tests {
		got := isEntryPoint(tt.name, tt.kind)
		if got != tt.want {
			t.Errorf("isEntryPoint(%q, %q) = %v, want %v", tt.name, tt.kind, got, tt.want)
		}
	}
}

func TestSemanticAnalysis_EmptyFiles(t *testing.T) {
	findings := SemanticAnalysis(nil, nil)
	if len(findings) != 0 {
		t.Errorf("expected no findings for nil files, got %d", len(findings))
	}

	findings = SemanticAnalysis([]FileInput{}, nil)
	if len(findings) != 0 {
		t.Errorf("expected no findings for empty files, got %d", len(findings))
	}
}
