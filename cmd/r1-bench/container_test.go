package main

import (
	"strings"
	"testing"
)

func TestGenerateDockerfile_HappyPath(t *testing.T) {
	body, err := GenerateDockerfile(DockerfileSpec{
		Mission: "seed-hello-easy",
		Agent:   "r1-antitrunc",
	})
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	s := string(body)
	checks := []string{
		"FROM golang:1.25-alpine AS builder",
		"FROM alpine:3.20 AS runtime",
		`r1.bench.mission="seed-hello-easy"`,
		`r1.bench.agent="r1-antitrunc"`,
		"go build -trimpath -o /out/r1-bench ./cmd/r1-bench",
		`"--mission", "seed-hello-easy"`,
		`"--agent", "r1-antitrunc"`,
	}
	for _, want := range checks {
		if !strings.Contains(s, want) {
			t.Errorf("Dockerfile missing %q\n--- BODY ---\n%s", want, s)
		}
	}
}

func TestGenerateDockerfile_EmptyMissionErrors(t *testing.T) {
	_, err := GenerateDockerfile(DockerfileSpec{Agent: "r1"})
	if err == nil {
		t.Errorf("empty Mission should error")
	}
}

func TestGenerateDockerfile_EmptyAgentErrors(t *testing.T) {
	_, err := GenerateDockerfile(DockerfileSpec{Mission: "x"})
	if err == nil {
		t.Errorf("empty Agent should error")
	}
}

func TestGenerateDockerfile_DefaultGoVersion(t *testing.T) {
	body, err := GenerateDockerfile(DockerfileSpec{Mission: "x", Agent: "r1"})
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	if !strings.Contains(string(body), "golang:1.25-alpine") {
		t.Errorf("default Go version missing: %s", string(body))
	}
}

func TestGenerateDockerfile_CustomGoVersion(t *testing.T) {
	body, err := GenerateDockerfile(DockerfileSpec{Mission: "x", Agent: "r1", GoVersion: "1.24"})
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	if !strings.Contains(string(body), "golang:1.24-alpine") {
		t.Errorf("custom Go version missing: %s", string(body))
	}
}
