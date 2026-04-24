package depgraph

import (
	"os"
	"path/filepath"
	"testing"
)

func setupTestRepo(t *testing.T) string {
	dir := t.TempDir()

	// main.go imports utils
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import "myapp/utils"

func main() {
	utils.Hello()
}
`), 0o600)

	// utils.go has no imports
	os.MkdirAll(filepath.Join(dir, "utils"), 0755)
	os.WriteFile(filepath.Join(dir, "utils", "helpers.go"), []byte(`package utils

func Hello() {}
`), 0o600)

	// config.go imports utils too
	os.WriteFile(filepath.Join(dir, "config.go"), []byte(`package main

import "myapp/utils"

func loadConfig() {
	utils.Hello()
}
`), 0o600)

	return dir
}

func TestBuild(t *testing.T) {
	dir := setupTestRepo(t)
	g, err := Build(dir, []string{".go"})
	if err != nil {
		t.Fatal(err)
	}

	if len(g.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) < 2 {
		t.Errorf("expected at least 2 edges, got %d", len(g.Edges))
	}
}

func TestDependencies(t *testing.T) {
	dir := setupTestRepo(t)
	g, _ := Build(dir, []string{".go"})

	deps := g.Dependencies("main.go")
	if len(deps) != 1 || deps[0] != "myapp/utils" {
		t.Errorf("expected [myapp/utils], got %v", deps)
	}
}

func TestLeaves(t *testing.T) {
	dir := setupTestRepo(t)
	g, _ := Build(dir, []string{".go"})

	leaves := g.Leaves()
	found := false
	for _, l := range leaves {
		if l == filepath.Join("utils", "helpers.go") {
			found = true
		}
	}
	if !found {
		t.Errorf("utils/helpers.go should be a leaf, got %v", leaves)
	}
}

func TestExtractGoImports(t *testing.T) {
	src := `package main

import (
	"fmt"
	"os"
	"myapp/utils"
)

import "strings"
`
	imports := extractGoImports(src)
	if len(imports) != 4 {
		t.Errorf("expected 4 imports, got %d: %v", len(imports), imports)
	}
}

func TestExtractPyImports(t *testing.T) {
	src := `import os
import sys
from pathlib import Path
from collections import defaultdict, OrderedDict
`
	imports := extractPyImports(src)
	if len(imports) < 4 {
		t.Errorf("expected at least 4 imports, got %d: %v", len(imports), imports)
	}
}

func TestExtractTSImports(t *testing.T) {
	src := `import { foo } from './utils'
import * as bar from 'lodash'
const x = require('path')
`
	imports := extractTSImports(src)
	if len(imports) != 3 {
		t.Errorf("expected 3 imports, got %d: %v", len(imports), imports)
	}
}

func TestExtractRustImports(t *testing.T) {
	src := `use std::io;
use crate::config;
mod helpers;
`
	imports := extractRustImports(src)
	if len(imports) != 3 {
		t.Errorf("expected 3 imports, got %d: %v", len(imports), imports)
	}
}

func TestShouldSkip(t *testing.T) {
	if !shouldSkip("vendor/pkg/foo.go") {
		t.Error("should skip vendor")
	}
	if !shouldSkip("node_modules/express/index.js") {
		t.Error("should skip node_modules")
	}
	if shouldSkip("internal/pkg/foo.go") {
		t.Error("should not skip internal")
	}
}

func TestStats(t *testing.T) {
	dir := setupTestRepo(t)
	g, _ := Build(dir, []string{".go"})
	s := g.Stats()
	if s == "" {
		t.Error("stats should not be empty")
	}
}

func TestDedup(t *testing.T) {
	result := dedup([]string{"a", "b", "a", "c", "b"})
	if len(result) != 3 {
		t.Errorf("expected 3, got %d", len(result))
	}
}

func TestRoots(t *testing.T) {
	g := &Graph{
		Nodes: map[string]*Node{
			"a": {Path: "a", Imports: []string{"b"}},
			"b": {Path: "b", Imports: nil},
		},
		Edges: []Edge{{From: "a", To: "b"}},
	}

	roots := g.Roots()
	if len(roots) != 1 || roots[0] != "a" {
		t.Errorf("expected [a] as root, got %v", roots)
	}
}

func TestDetectCycles(t *testing.T) {
	g := &Graph{
		Nodes: map[string]*Node{
			"a": {Path: "a", Imports: []string{"b"}},
			"b": {Path: "b", Imports: []string{"a"}},
		},
		Edges: []Edge{
			{From: "a", To: "b"},
			{From: "b", To: "a"},
		},
	}

	cycles := g.DetectCycles()
	if len(cycles) == 0 {
		t.Error("should detect cycle between a and b")
	}
}
