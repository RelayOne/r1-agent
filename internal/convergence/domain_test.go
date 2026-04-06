package convergence

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectDomains_GoMod(t *testing.T) {
	dir := t.TempDir()
	gomod := `module example.com/myapp

go 1.22

require (
	github.com/lib/pq v1.10.9
	github.com/redis/go-redis/v9 v9.5.1
	github.com/stripe/stripe-go/v76 v76.0.0
	google.golang.org/grpc v1.62.0
)
`
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0644)

	domains := DetectDomains(dir)

	expected := []Domain{DomainPostgres, DomainRedis, DomainStripe, DomainGRPC}
	for _, d := range expected {
		if !domains[d] {
			t.Errorf("expected domain %s to be detected", d)
		}
	}

	unexpected := []Domain{DomainKafka, DomainMySQL, DomainMongoDB}
	for _, d := range unexpected {
		if domains[d] {
			t.Errorf("did not expect domain %s to be detected", d)
		}
	}
}

func TestDetectDomains_PackageJSON(t *testing.T) {
	dir := t.TempDir()
	pkg := `{
  "name": "my-app",
  "dependencies": {
    "react": "^18.2.0",
    "next": "^14.0.0",
    "stripe": "^14.0.0",
    "ioredis": "^5.0.0"
  }
}`
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644)

	domains := DetectDomains(dir)

	expected := []Domain{DomainReact, DomainNextJS, DomainStripe, DomainRedis}
	for _, d := range expected {
		if !domains[d] {
			t.Errorf("expected domain %s to be detected", d)
		}
	}
}

func TestDetectDomains_FileSignals(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine"), 0644)
	os.Mkdir(filepath.Join(dir, "terraform"), 0755)

	domains := DetectDomains(dir)

	if !domains[DomainDocker] {
		t.Error("expected Docker domain from Dockerfile")
	}
	if !domains[DomainTerraform] {
		t.Error("expected Terraform domain from terraform/ dir")
	}
}

func TestDetectDomains_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	domains := DetectDomains(dir)
	if len(domains) != 0 {
		t.Errorf("expected no domains in empty dir, got %d", len(domains))
	}
}

func TestDomainRules_NoDomainsReturnsEmpty(t *testing.T) {
	rules := DomainRules(map[Domain]bool{})
	if len(rules) != 0 {
		t.Errorf("expected 0 domain rules for no domains, got %d", len(rules))
	}
}

func TestDomainRules_PostgresActivation(t *testing.T) {
	rules := DomainRules(map[Domain]bool{DomainPostgres: true})
	if len(rules) == 0 {
		t.Fatal("expected SQL rules for Postgres domain")
	}
	ids := make(map[string]bool)
	for _, r := range rules {
		ids[r.ID] = true
	}
	expected := []string{"sql-no-select-star", "sql-no-cascade-delete", "sql-transaction-timeout", "sql-migration-index"}
	for _, id := range expected {
		if !ids[id] {
			t.Errorf("expected rule %s for Postgres domain", id)
		}
	}
}

func TestDomainRules_StripeActivation(t *testing.T) {
	rules := DomainRules(map[Domain]bool{DomainStripe: true})
	ids := make(map[string]bool)
	for _, r := range rules {
		ids[r.ID] = true
	}
	if !ids["stripe-webhook-verify"] || !ids["stripe-idempotency"] {
		t.Error("expected stripe webhook and idempotency rules")
	}
}

func TestDomainRules_DockerActivation(t *testing.T) {
	rules := DomainRules(map[Domain]bool{DomainDocker: true})
	ids := make(map[string]bool)
	for _, r := range rules {
		ids[r.ID] = true
	}
	if !ids["docker-no-root"] {
		t.Error("expected docker-no-root rule")
	}
}

func TestDomainRules_MultipleDomains(t *testing.T) {
	rules := DomainRules(map[Domain]bool{
		DomainPostgres: true,
		DomainRedis:    true,
		DomainKafka:    true,
		DomainStripe:   true,
		DomainDocker:   true,
		DomainGRPC:     true,
	})
	if len(rules) < 10 {
		t.Errorf("expected at least 10 rules for multi-domain project, got %d", len(rules))
	}
}

func TestNewValidatorForProject(t *testing.T) {
	dir := t.TempDir()
	// Create a go.mod with Kafka dependency
	gomod := `module test
go 1.22
require github.com/IBM/sarama v1.0.0
`
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0644)

	v := NewValidatorForProject(dir)
	// Should have base rules + Kafka/messaging rules
	v.mu.RLock()
	ruleCount := len(v.rules)
	v.mu.RUnlock()

	// Base is 66, Kafka adds messaging rules (2)
	if ruleCount <= 66 {
		t.Errorf("expected more than 66 rules with Kafka domain, got %d", ruleCount)
	}

	// Verify messaging rules are present
	v.mu.RLock()
	ids := make(map[string]bool)
	for _, r := range v.rules {
		ids[r.ID] = true
	}
	v.mu.RUnlock()

	if !ids["msg-idempotent-consumer"] {
		t.Error("expected msg-idempotent-consumer rule for Kafka project")
	}
}

func TestDomainRulesFire(t *testing.T) {
	tests := []struct {
		name    string
		domain  Domain
		file    string
		content string
		ruleID  string
		wantHit bool
	}{
		{
			name:    "SELECT * detected",
			domain:  DomainPostgres,
			file:    "repo.go",
			content: `rows, err := db.Query("SELECT * FROM users WHERE id = $1", id)`,
			ruleID:  "sql-no-select-star",
			wantHit: true,
		},
		{
			name:    "explicit columns OK",
			domain:  DomainPostgres,
			file:    "repo.go",
			content: `rows, err := db.Query("SELECT id, name FROM users WHERE id = $1", id)`,
			ruleID:  "sql-no-select-star",
			wantHit: false,
		},
		{
			name:    "Dockerfile without USER",
			domain:  DomainDocker,
			file:    "Dockerfile",
			content: "FROM alpine:3.18\nRUN apk add --no-cache ca-certificates\nCMD [\"/app\"]\n",
			ruleID:  "docker-no-root",
			wantHit: true,
		},
		{
			name:    "Dockerfile with USER",
			domain:  DomainDocker,
			file:    "Dockerfile",
			content: "FROM alpine:3.18\nRUN apk add --no-cache ca-certificates\nUSER 1000\nCMD [\"/app\"]\n",
			ruleID:  "docker-no-root",
			wantHit: false,
		},
		{
			name:    "Redis KEYS command",
			domain:  DomainRedis,
			file:    "cache.go",
			content: `keys, err := client.Keys(ctx, "user:*").Result()`,
			ruleID:  "redis-no-keys-cmd",
			wantHit: true,
		},
		{
			name:    "K8s deployment without limits",
			domain:  DomainK8s,
			file:    "deployment.yaml",
			content: "apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n      - name: app\n",
			ruleID:  "k8s-resource-limits",
			wantHit: true,
		},
		{
			name:    "K8s deployment with limits",
			domain:  DomainK8s,
			file:    "deployment.yaml",
			content: "apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n      - name: app\n        resources:\n          limits:\n            cpu: 500m\n            memory: 128Mi\n",
			ruleID:  "k8s-resource-limits",
			wantHit: false,
		},
		{
			name:    "Terraform hardcoded creds",
			domain:  DomainTerraform,
			file:    "main.tf",
			content: `access_key = "AKIAIOSFODNN7EXAMPLE"`,
			ruleID:  "tf-no-hardcoded-creds",
			wantHit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := DomainRules(map[Domain]bool{tt.domain: true})
			var targetRule *Rule
			for i := range rules {
				if rules[i].ID == tt.ruleID {
					targetRule = &rules[i]
					break
				}
			}
			if targetRule == nil {
				t.Fatalf("rule %s not found in domain %s rules", tt.ruleID, tt.domain)
			}

			findings := targetRule.Check(tt.file, []byte(tt.content))
			if tt.wantHit && len(findings) == 0 {
				t.Errorf("expected rule %s to fire but got no findings", tt.ruleID)
			}
			if !tt.wantHit && len(findings) > 0 {
				t.Errorf("expected rule %s not to fire but got %d findings", tt.ruleID, len(findings))
			}
		})
	}
}
