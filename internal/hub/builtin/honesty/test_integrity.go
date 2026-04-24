// Package honesty implements the 7-layer Honesty Judge as hub subscribers.
// Each layer detects a different class of AI agent deception, from structural
// violations (Layer 1) to impossible task canaries (Layer 7).
package honesty

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/RelayOne/r1/internal/hub"
)

// TestIntegrityChecker is a gate subscriber that records the AST shape of every
// test file before any agent edit. When a test file is about to be written, it
// compares the new shape to the original to detect test weakening.
type TestIntegrityChecker struct {
	snapshots map[string]*TestSnapshot
	mu        sync.RWMutex
}

// NewTestIntegrityChecker creates a new test integrity checker.
func NewTestIntegrityChecker() *TestIntegrityChecker {
	return &TestIntegrityChecker{
		snapshots: make(map[string]*TestSnapshot),
	}
}

// TestSnapshot records the shape of a test file for comparison.
type TestSnapshot struct {
	Path           string
	OriginalLines  int
	AssertionCount int
	TestFuncCount  int
	Hash           string
}

// Register adds the test integrity checker to the hub.
func (t *TestIntegrityChecker) Register(bus *hub.Bus) {
	bus.Register(hub.Subscriber{
		ID:       "builtin.honesty.test_integrity",
		Events:   []hub.EventType{hub.EventToolPreUse},
		Mode:     hub.ModeGateStrict,
		Priority: 90,
		Handler:  t.handle,
	})
}

func (t *TestIntegrityChecker) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	if ev.Tool == nil {
		return &hub.HookResponse{Decision: hub.Allow}
	}
	// Only check file write operations
	name := ev.Tool.Name
	if name != "write" && name != "edit" && name != "str_replace_editor" {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	path, _ := ev.Tool.Input["path"].(string)
	if path == "" {
		path, _ = ev.Tool.Input["file_path"].(string)
	}
	if !isTestFile(path) {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	content, _ := ev.Tool.Input["content"].(string)
	newString, _ := ev.Tool.Input["new_string"].(string)
	oldContent, _ := ev.Tool.Input["old_content"].(string)

	// For edit operations, we check the new_string for weakening patterns
	if name == "edit" || name == "str_replace_editor" {
		if newString != "" {
			return t.checkEditWeakening(path, newString, oldContent)
		}
		return &hub.HookResponse{Decision: hub.Allow}
	}

	// For write operations, compare against snapshot
	if content == "" {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	// Take baseline snapshot if we don't have one
	t.mu.RLock()
	snap, exists := t.snapshots[path]
	t.mu.RUnlock()

	if !exists && oldContent != "" {
		snap = takeSnapshot(path, oldContent)
		t.mu.Lock()
		t.snapshots[path] = snap
		t.mu.Unlock()
	}

	if snap == nil {
		// First time seeing this file, take initial snapshot
		snap = takeSnapshot(path, content)
		t.mu.Lock()
		t.snapshots[path] = snap
		t.mu.Unlock()
		return &hub.HookResponse{Decision: hub.Allow}
	}

	// Compare new content to snapshot
	newSnap := takeSnapshot(path, content)
	if newSnap.AssertionCount < snap.AssertionCount {
		return &hub.HookResponse{
			Decision: hub.Deny,
			Reason: fmt.Sprintf("test file assertion count decreased from %d to %d — possible test weakening",
				snap.AssertionCount, newSnap.AssertionCount),
		}
	}
	if newSnap.TestFuncCount < snap.TestFuncCount {
		return &hub.HookResponse{
			Decision: hub.Deny,
			Reason: fmt.Sprintf("test function count decreased from %d to %d — possible test removal",
				snap.TestFuncCount, newSnap.TestFuncCount),
		}
	}
	return &hub.HookResponse{Decision: hub.Allow}
}

func (t *TestIntegrityChecker) checkEditWeakening(path, newString, oldContent string) *hub.HookResponse {
	// Check for common weakening patterns in the replacement
	weakeningPatterns := []string{
		"_ = ", // blank identifier to suppress errors
		"// TODO",
		"// FIXME",
		"pass\n", // Python pass
	}
	for _, pat := range weakeningPatterns {
		if strings.Contains(newString, pat) {
			return &hub.HookResponse{
				Decision: hub.Deny,
				Reason:   fmt.Sprintf("test edit contains weakening pattern: %q", pat),
			}
		}
	}
	return &hub.HookResponse{Decision: hub.Allow}
}

// RecordSnapshot records the initial state of a test file for later comparison.
func (t *TestIntegrityChecker) RecordSnapshot(path, content string) {
	if !isTestFile(path) {
		return
	}
	snap := takeSnapshot(path, content)
	t.mu.Lock()
	t.snapshots[path] = snap
	t.mu.Unlock()
}

func takeSnapshot(path, content string) *TestSnapshot {
	snap := &TestSnapshot{
		Path:          path,
		OriginalLines: strings.Count(content, "\n"),
	}
	switch {
	case strings.HasSuffix(path, "_test.go"):
		countGoTestSignals(content, snap)
	case isJSTestFile(path):
		countJSTestSignals(content, snap)
	case isPyTestFile(path):
		countPyTestSignals(content, snap)
	default:
		countByRegex(content, snap)
	}
	h := sha256.Sum256([]byte(content))
	snap.Hash = hex.EncodeToString(h[:])
	return snap
}

func countGoTestSignals(content string, snap *TestSnapshot) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", content, 0)
	if err != nil {
		countByRegex(content, snap)
		return
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if !strings.HasPrefix(fd.Name.Name, "Test") && !strings.HasPrefix(fd.Name.Name, "Benchmark") {
			continue
		}
		snap.TestFuncCount++
		ast.Inspect(fd, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if isAssertionMethod(sel.Sel.Name) {
						snap.AssertionCount++
					}
				}
			}
			return true
		})
	}
}

func isAssertionMethod(name string) bool {
	switch name {
	case "Equal", "EqualError", "Equals", "True", "False", "Nil", "NotNil",
		"Error", "NoError", "ErrorIs", "ErrorAs", "Contains", "NotContains",
		"Empty", "NotEmpty", "Len", "Greater", "Less", "Panics",
		"Errorf", "Fatalf", "Fatal":
		return true
	}
	return false
}

func countJSTestSignals(content string, snap *TestSnapshot) {
	snap.TestFuncCount = strings.Count(content, "test(") +
		strings.Count(content, "it(") + strings.Count(content, "describe(")
	snap.AssertionCount = strings.Count(content, "expect(") +
		strings.Count(content, "assert.")
}

func countPyTestSignals(content string, snap *TestSnapshot) {
	snap.TestFuncCount = strings.Count(content, "def test_")
	snap.AssertionCount = strings.Count(content, "assert ")
}

func countByRegex(content string, snap *TestSnapshot) {
	snap.AssertionCount = strings.Count(content, "assert")
	// Count test-like function patterns
	testFuncRe := regexp.MustCompile(`(?m)^func\s+Test|^def\s+test_|^\s*(it|test|describe)\(`)
	snap.TestFuncCount = len(testFuncRe.FindAllString(content, -1))
}

func isTestFile(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(path, "_test.go") ||
		isJSTestFile(path) ||
		isPyTestFile(path) ||
		strings.HasSuffix(path, "_test.rs") ||
		strings.HasPrefix(base, "test_")
}

func isJSTestFile(path string) bool {
	for _, suffix := range []string{
		".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx",
		".test.js", ".test.jsx", ".spec.js", ".spec.jsx",
	} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func isPyTestFile(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(path, "_test.py") || strings.HasPrefix(base, "test_")
}

