package main

import (
	"strings"
	"testing"
)

func TestParseConfig_ValidConfig(t *testing.T) {
	cfg := &Config{
		Host: "localhost",
		Port: 5432,
		Database: &DatabaseConfig{
			DSN:         "postgres://localhost/testdb",
			MaxPoolSize: 5,
		},
	}

	result, err := ParseConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "host=localhost") {
		t.Error("expected result to contain host=localhost")
	}
	if !strings.Contains(result, "port=5432") {
		t.Error("expected result to contain port=5432")
	}
	if !strings.Contains(result, "dsn=postgres://localhost/testdb") {
		t.Error("expected result to contain dsn")
	}
}

func TestParseConfig_NilDatabase(t *testing.T) {
	cfg := &Config{
		Host: "example.com",
		Port: 3000,
	}

	result, err := ParseConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "host=example.com") {
		t.Error("expected result to contain host=example.com")
	}
	if strings.Contains(result, "dsn=") {
		t.Error("expected no dsn when Database is nil")
	}
}
