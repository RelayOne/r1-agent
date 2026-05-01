package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExtractMissingNpmPackages_CommonPatterns(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   []string
	}{
		{
			"cannot find module - single quotes",
			"Error: Cannot find module 'zod'",
			[]string{"zod"},
		},
		{
			"cannot find module - double quotes",
			`Error: Cannot find module "react"`,
			[]string{"react"},
		},
		{
			"ts2307 typescript error",
			`task.ts:1:22 - error TS2307: Cannot find module 'zod' or its corresponding type declarations.`,
			[]string{"zod"},
		},
		{
			"err_module_not_found with path",
			`Error [ERR_MODULE_NOT_FOUND]: Cannot find package 'express' imported from /app/server.js`,
			[]string{"express"},
		},
		{
			"multiple packages dedup",
			`Cannot find module 'zod'
Cannot find module 'zod'
Cannot find module 'react'`,
			[]string{"react", "zod"},
		},
		{
			"scoped package retained",
			"Cannot find module '@anthropic/sdk'",
			[]string{"@anthropic/sdk"},
		},
		{
			"scoped package with deep subpath collapses to scope+name",
			"Cannot find module '@repo/types/schemas/user'",
			[]string{"@repo/types"},
		},
		{
			"relative path dropped",
			"Cannot find module './local-helper'",
			nil,
		},
		{
			"absolute path dropped",
			`Cannot find module "/usr/lib/foo"`,
			nil,
		},
		{
			"node: builtin dropped",
			"Cannot find module 'node:fs'",
			nil,
		},
		{
			"vite module not found format",
			`Module not found: Can't resolve 'vitest'`,
			[]string{"vitest"},
		},
		{
			"empty stderr",
			"",
			nil,
		},
		{
			"no missing-module pattern",
			"some random unrelated error\n",
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractMissingNpmPackages(c.stderr)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestPackageRootFromImport(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"zod", "zod"},
		{"zod/schemas", "zod"},
		{"@anthropic/sdk", "@anthropic/sdk"},
		{"@anthropic/sdk/messages", "@anthropic/sdk"},
		{"@scope/pkg/deep/nested/path", "@scope/pkg"},
		{"@scope", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := packageRootFromImport(c.in); got != c.want {
			t.Errorf("packageRootFromImport(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAddRootDevDeps_AddsNewPackages(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "package.json")
	initial := `{
  "name": "root",
  "version": "1.0.0",
  "devDependencies": {
    "typescript": "^5.0.0"
  }
}`
	if err := os.WriteFile(pkg, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := addRootDevDeps(dir, []string{"zod", "react"}); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(pkg)
	var obj map[string]any
	_ = json.Unmarshal(raw, &obj)
	devDeps, _ := obj["devDependencies"].(map[string]any)

	if devDeps["zod"] != "*" {
		t.Errorf("zod = %v, want *", devDeps["zod"])
	}
	if devDeps["react"] != "*" {
		t.Errorf("react = %v, want *", devDeps["react"])
	}
	if devDeps["typescript"] != "^5.0.0" {
		t.Errorf("typescript version was clobbered: %v", devDeps["typescript"])
	}
}

func TestAddRootDevDeps_SkipsDuplicates(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "package.json")
	initial := `{
  "name": "root",
  "dependencies": {
    "zod": "^3.22.0"
  },
  "devDependencies": {
    "typescript": "^5.0.0"
  }
}`
	_ = os.WriteFile(pkg, []byte(initial), 0o600)

	// zod exists in deps → skipped even though we're adding to devDeps.
	// typescript exists in devDeps → skipped.
	// Only react is truly new.
	if err := addRootDevDeps(dir, []string{"zod", "typescript", "react"}); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(pkg)
	var obj map[string]any
	_ = json.Unmarshal(raw, &obj)

	// deps unchanged
	deps, _ := obj["dependencies"].(map[string]any)
	if deps["zod"] != "^3.22.0" {
		t.Errorf("zod version was changed: %v", deps["zod"])
	}

	// devDeps gained react only
	devDeps, _ := obj["devDependencies"].(map[string]any)
	if devDeps["typescript"] != "^5.0.0" {
		t.Errorf("typescript version was changed: %v", devDeps["typescript"])
	}
	if devDeps["react"] != "*" {
		t.Errorf("react should have been added as *, got %v", devDeps["react"])
	}
	if _, dupe := devDeps["zod"]; dupe {
		t.Error("zod should NOT have been duplicated into devDeps (already in deps)")
	}
}

func TestAddRootDevDeps_CreatesDevDepsIfMissing(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "package.json")
	_ = os.WriteFile(pkg, []byte(`{"name":"root"}`), 0o600)

	if err := addRootDevDeps(dir, []string{"zod"}); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(pkg)
	if !strings.Contains(string(raw), `"devDependencies"`) {
		t.Error("devDependencies block should have been created")
	}
	if !strings.Contains(string(raw), `"zod"`) {
		t.Error("zod should be in the output")
	}
}

func TestAddRootDevDeps_NoChangeIsNoop(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "package.json")
	initial := `{"name":"root","devDependencies":{"zod":"^3.22.0"}}`
	_ = os.WriteFile(pkg, []byte(initial), 0o600)
	info1, _ := os.Stat(pkg)

	// Adding an already-declared pkg should not rewrite the file.
	if err := addRootDevDeps(dir, []string{"zod"}); err != nil {
		t.Fatal(err)
	}

	info2, _ := os.Stat(pkg)
	if info2.Size() != info1.Size() {
		t.Errorf("file was rewritten when no change was needed (size %d → %d)", info1.Size(), info2.Size())
	}
}

func TestAddRootDevDeps_MissingPackageJson(t *testing.T) {
	dir := t.TempDir()
	// No package.json.
	err := addRootDevDeps(dir, []string{"zod"})
	if err == nil {
		t.Error("expected error for missing package.json")
	}
}

func TestAddRootDevDeps_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "package.json")
	_ = os.WriteFile(pkg, []byte(`{ "not valid json`), 0o600)
	err := addRootDevDeps(dir, []string{"zod"})
	if err == nil {
		t.Error("expected error for corrupt package.json")
	}
}
