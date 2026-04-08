package skillselect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectGoPostgresProject(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte(`module example.com/app

go 1.22

require github.com/jackc/pgx/v5 v5.5.0
`), 0644)

	p, err := DetectProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !containsProfile(p.Languages, "go") {
		t.Error("expected go language")
	}
	if !containsProfile(p.Databases, "postgres") {
		t.Error("expected postgres database")
	}
}

func TestDetectNextJSWithPrisma(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"dependencies": {
			"next": "14.0.0",
			"react": "18.0.0",
			"@prisma/client": "5.0.0",
			"pg": "8.0.0"
		}
	}`), 0644)

	p, err := DetectProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !containsProfile(p.Frameworks, "nextjs") {
		t.Error("expected nextjs")
	}
	if !containsProfile(p.Frameworks, "react") {
		t.Error("expected react")
	}
	if !containsProfile(p.Frameworks, "prisma") {
		t.Error("expected prisma")
	}
	if !containsProfile(p.Databases, "postgres") {
		t.Error("expected postgres from pg dep")
	}
}

func TestDetectMonorepo(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "turbo.json"), []byte(`{}`), 0644)
	os.MkdirAll(filepath.Join(dir, "apps", "web"), 0755)
	os.WriteFile(filepath.Join(dir, "apps", "web", "package.json"), []byte(`{
		"dependencies": {"react": "18.0.0"}
	}`), 0644)
	os.MkdirAll(filepath.Join(dir, "apps", "api"), 0755)
	os.WriteFile(filepath.Join(dir, "apps", "api", "go.mod"), []byte(`module example.com/api
go 1.22
`), 0644)

	p, err := DetectProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasMonorepo {
		t.Error("expected monorepo detection")
	}
	if !containsProfile(p.Languages, "go") {
		t.Error("expected go from apps/api")
	}
	if !containsProfile(p.Frameworks, "react") {
		t.Error("expected react from apps/web")
	}
}

func TestDetectDockerCompose(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(`
services:
  db:
    image: postgres:16
  cache:
    image: redis:7
  queue:
    image: apache/kafka:3.7
`), 0644)

	p, err := DetectProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !containsProfile(p.Databases, "postgres") {
		t.Error("expected postgres from compose")
	}
	if !containsProfile(p.Databases, "redis") {
		t.Error("expected redis from compose")
	}
	if !containsProfile(p.MessageQueues, "kafka") {
		t.Error("expected kafka from compose")
	}
	if !p.HasDocker {
		t.Error("expected HasDocker")
	}
}

func TestDetectCloudflareWorkers(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "wrangler.toml"), []byte(`name = "my-worker"`), 0644)

	p, err := DetectProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !containsProfile(p.CloudProviders, "cloudflare") {
		t.Error("expected cloudflare")
	}
}

func TestDetectCIPlatforms(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0755)
	os.WriteFile(filepath.Join(dir, ".github", "workflows", "test.yml"), []byte("name: test"), 0644)

	p, err := DetectProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasCI {
		t.Error("expected HasCI")
	}
	if !containsProfile(p.CIPlatforms, "github-actions") {
		t.Error("expected github-actions")
	}
}

func TestMatchSkillsRanking(t *testing.T) {
	p := &RepoProfile{
		Languages: []string{"go"},
		Databases: []string{"postgres"},
	}
	matches := MatchSkills(p)
	if len(matches) == 0 {
		t.Fatal("expected matches")
	}
	// agent-discipline should be first (score 1000)
	if matches[0] != "agent-discipline" {
		t.Errorf("first match=%s, want agent-discipline", matches[0])
	}
	// go and postgres should be in matches
	foundGo, foundPg := false, false
	for _, m := range matches {
		if m == "go" {
			foundGo = true
		}
		if m == "postgres" {
			foundPg = true
		}
	}
	if !foundGo {
		t.Error("expected go in matches")
	}
	if !foundPg {
		t.Error("expected postgres in matches")
	}
}

func TestMatchSkillsNil(t *testing.T) {
	if matches := MatchSkills(nil); matches != nil {
		t.Errorf("expected nil, got %v", matches)
	}
}

func TestRepoProfileToStackInfo(t *testing.T) {
	p := &RepoProfile{
		Languages:      []string{"go"},
		Frameworks:     []string{"gin"},
		Databases:      []string{"postgres"},
		CloudProviders: []string{"gcp"},
		InfraTools:     []string{"docker"},
	}
	si := p.ToStackInfo()
	if !si.HasLanguage("go") {
		t.Error("expected go")
	}
	if !si.HasFramework("gin") {
		t.Error("expected gin")
	}
	if !si.HasDatabase("postgres") {
		t.Error("expected postgres")
	}
}

func TestRepoProfileTags(t *testing.T) {
	p := &RepoProfile{
		Languages: []string{"Go", "TypeScript"},
		Databases: []string{"postgres"},
	}
	tags := p.Tags()
	if len(tags) != 3 {
		t.Errorf("expected 3 tags, got %d: %v", len(tags), tags)
	}
}

func TestDetectEnvFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env.example"), []byte(`
DATABASE_URL=postgresql://localhost/mydb
REDIS_URL=redis://localhost:6379
KAFKA_BROKERS=localhost:9092
`), 0644)

	p, err := DetectProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !containsProfile(p.Databases, "postgres") {
		t.Error("expected postgres from .env")
	}
	if !containsProfile(p.Databases, "redis") {
		t.Error("expected redis from .env")
	}
	if !containsProfile(p.MessageQueues, "kafka") {
		t.Error("expected kafka from .env")
	}
}

func TestDetectPythonProject(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`
[tool.poetry]
name = "myapp"

[tool.poetry.dependencies]
django = "^5.0"
psycopg2 = "^2.9"
boto3 = "^1.34"

[tool.uv]
package = true
`), 0644)

	p, err := DetectProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !containsProfile(p.Languages, "python") {
		t.Error("expected python")
	}
	if !containsProfile(p.Frameworks, "django") {
		t.Error("expected django")
	}
	if !containsProfile(p.Databases, "postgres") {
		t.Error("expected postgres from psycopg2")
	}
	if !containsProfile(p.CloudProviders, "aws") {
		t.Error("expected aws from boto3")
	}
	if !containsProfile(p.PackageManagers, "poetry") {
		t.Error("expected poetry")
	}
	if !containsProfile(p.PackageManagers, "uv") {
		t.Error("expected uv")
	}
}

func containsProfile(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
