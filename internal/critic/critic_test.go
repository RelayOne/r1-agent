package critic

import (
	"strings"
	"testing"
)

func TestCleanCode(t *testing.T) {
	c := New(Config{})
	v := c.ReviewFile("main.go", `package main

func Run() error {
	return nil
}
`)
	if !v.Pass {
		t.Error("clean code should pass")
	}
	if v.Score < 0.9 {
		t.Errorf("clean code should score high, got %f", v.Score)
	}
}

func TestDetectSecret(t *testing.T) {
	c := New(Config{})
	v := c.ReviewFile("config.go", `package config

const apiKey = "sk-abcdefghijklmnopqrstuvwxyz123456"
`)
	if v.Pass {
		t.Error("hardcoded secret should block")
	}
	found := false
	for _, f := range v.Findings {
		if f.Category == "security" && f.Severity == SeverityBlock {
			found = true
		}
	}
	if !found {
		t.Error("should find security/block finding")
	}
}

func TestDetectAWSKey(t *testing.T) {
	c := New(Config{})
	v := c.ReviewFile("aws.go", `package aws

var key = "AKIAIOSFODNN7EXAMPLE"
`)
	hasSecret := false
	for _, f := range v.Findings {
		if f.Rule == "no-hardcoded-secrets" {
			hasSecret = true
		}
	}
	if !hasSecret {
		t.Error("should detect AWS key")
	}
}

func TestDetectDebugPrint(t *testing.T) {
	c := New(Config{})
	v := c.ReviewFile("handler.go", `package handler

import "fmt"

func Handle() {
	fmt.Println("debug: got here")
}
`)
	found := false
	for _, f := range v.Findings {
		if f.Rule == "no-fmt-print" {
			found = true
		}
	}
	if !found {
		t.Error("should detect debug print")
	}
}

func TestDebugPrintOKInTest(t *testing.T) {
	c := New(Config{})
	v := c.ReviewFile("handler_test.go", `package handler

import "fmt"

func TestHandle() {
	fmt.Println("test output")
}
`)
	for _, f := range v.Findings {
		if f.Rule == "no-fmt-print" {
			t.Error("should not flag fmt.Println in test files")
		}
	}
}

func TestDetectPanic(t *testing.T) {
	c := New(Config{})
	v := c.ReviewFile("lib.go", `package lib

func Init() {
	panic("not implemented")
}
`)
	found := false
	for _, f := range v.Findings {
		if f.Rule == "no-panic" {
			found = true
		}
	}
	if !found {
		t.Error("should detect panic in library code")
	}
}

func TestPanicOKInMain(t *testing.T) {
	c := New(Config{})
	v := c.ReviewFile("main.go", `package main

func main() {
	panic("fatal")
}
`)
	for _, f := range v.Findings {
		if f.Rule == "no-panic" {
			t.Error("should allow panic in main.go")
		}
	}
}

func TestDetectSQLInjection(t *testing.T) {
	c := New(Config{})
	v := c.ReviewFile("db.go", `package db

func GetUser(id string) {
	db.Query("SELECT * FROM users WHERE id = " + id)
}
`)
	if v.Pass {
		t.Error("SQL injection should block")
	}
}

func TestBlockOnWarn(t *testing.T) {
	c := New(Config{BlockOnWarn: true})
	v := c.ReviewFile("handler.go", `package handler

import "fmt"

func Handle() {
	fmt.Println("debug")
}
`)
	if v.Pass {
		t.Error("should block when BlockOnWarn=true and warnings exist")
	}
}

func TestMinScore(t *testing.T) {
	c := New(Config{MinScore: 0.99})
	v := c.ReviewFile("code.go", `package code

// TODO: implement this
func Stub() {}
`)
	if v.Pass {
		t.Error("should fail with high MinScore and any findings")
	}
}

func TestMaxFindings(t *testing.T) {
	c := New(Config{MaxFindings: 1})
	v := c.Review(map[string]string{
		"a.go": "package a\n// TODO: one\n// TODO: two\n// FIXME: three\n",
	})
	if v.Pass {
		t.Error("should block when exceeding MaxFindings")
	}
}

func TestFormatFindings(t *testing.T) {
	c := New(Config{})
	v := c.ReviewFile("code.go", `package code

const token = "ghp_abcdefghijklmnopqrstuvwxyz1234567890"
`)
	output := FormatFindings(v)
	if !strings.Contains(output, "BLOCK") {
		t.Error("should contain BLOCK section")
	}
	if !strings.Contains(output, "security") {
		t.Error("should mention security category")
	}
}

func TestFormatFindingsClean(t *testing.T) {
	v := &Verdict{Pass: true}
	output := FormatFindings(v)
	if !strings.Contains(output, "No issues") {
		t.Error("clean verdict should say no issues")
	}
}

func TestMultiFileReview(t *testing.T) {
	c := New(Config{})
	v := c.Review(map[string]string{
		"a.go": "package a\nfunc A() error { return nil }\n",
		"b.go": "package b\n// TODO: fix this\n",
	})
	if len(v.Findings) == 0 {
		t.Error("should find TODO in b.go")
	}
}

func TestCustomRule(t *testing.T) {
	c := New(Config{
		Rules: []Rule{{
			ID: "no-foo", Name: "No foo allowed", Severity: SeverityBlock,
			Check: func(file, content string) []Finding {
				if strings.Contains(content, "foo") {
					return []Finding{{Severity: SeverityBlock, Category: "custom", File: file, Message: "found foo"}}
				}
				return nil
			},
		}},
	})
	v := c.ReviewFile("test.go", "package test\nvar foo = 1\n")
	if v.Pass {
		t.Error("custom rule should block on foo")
	}
}

func TestVerdictSummary(t *testing.T) {
	v := &Verdict{Pass: true, Score: 1.0}
	s := buildSummary(v)
	if s == "" {
		t.Error("summary should not be empty")
	}
}

func TestLargeFunction(t *testing.T) {
	// Generate a 150-line function
	var b strings.Builder
	b.WriteString("package big\n\nfunc BigFunc() {\n")
	for i := 0; i < 120; i++ {
		b.WriteString("\tx := 1\n")
	}
	b.WriteString("}\n")

	c := New(Config{})
	v := c.ReviewFile("big.go", b.String())
	found := false
	for _, f := range v.Findings {
		if f.Rule == "large-function" {
			found = true
		}
	}
	if !found {
		t.Error("should detect large function")
	}
}
