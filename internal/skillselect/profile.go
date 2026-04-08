package skillselect

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// RepoProfile is the enriched technology profile of a repository.
// It extends StackInfo with additional fields for message queues, protocols,
// build tools, package managers, test frameworks, CI platforms, and confidence.
type RepoProfile struct {
	Languages       []string           `json:"languages"`
	Frameworks      []string           `json:"frameworks"`
	Databases       []string           `json:"databases"`
	MessageQueues   []string           `json:"message_queues"`
	CloudProviders  []string           `json:"cloud_providers"`
	InfraTools      []string           `json:"infra_tools"`
	Protocols       []string           `json:"protocols"`
	BuildTools      []string           `json:"build_tools"`
	PackageManagers []string           `json:"package_managers"`
	TestFrameworks  []string           `json:"test_frameworks"`
	CIPlatforms     []string           `json:"ci_platforms"`
	HasMonorepo     bool               `json:"has_monorepo"`
	HasDocker       bool               `json:"has_docker"`
	HasCI           bool               `json:"has_ci"`
	Confidence      map[string]float64 `json:"confidence"`
}

// ToStackInfo converts a RepoProfile to the simpler StackInfo for backward compatibility.
func (p *RepoProfile) ToStackInfo() *StackInfo {
	infra := make([]string, len(p.InfraTools))
	copy(infra, p.InfraTools)
	return &StackInfo{
		Languages:      p.Languages,
		Frameworks:     p.Frameworks,
		Databases:      p.Databases,
		CloudProviders: p.CloudProviders,
		Infra:          infra,
	}
}

// Tags returns a deduplicated sorted list of all detected technologies.
func (p *RepoProfile) Tags() []string {
	all := make(map[string]bool)
	for _, s := range p.Languages {
		all[strings.ToLower(s)] = true
	}
	for _, s := range p.Frameworks {
		all[strings.ToLower(s)] = true
	}
	for _, s := range p.Databases {
		all[strings.ToLower(s)] = true
	}
	for _, s := range p.MessageQueues {
		all[strings.ToLower(s)] = true
	}
	for _, s := range p.CloudProviders {
		all[strings.ToLower(s)] = true
	}
	for _, s := range p.InfraTools {
		all[strings.ToLower(s)] = true
	}
	for _, s := range p.Protocols {
		all[strings.ToLower(s)] = true
	}
	for _, s := range p.BuildTools {
		all[strings.ToLower(s)] = true
	}
	for _, s := range p.TestFrameworks {
		all[strings.ToLower(s)] = true
	}
	var out []string
	for k := range all {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DetectProfile scans a repository root and builds a RepoProfile.
// Returns a partially filled profile even on errors — detection is best-effort.
func DetectProfile(root string) (*RepoProfile, error) {
	p := &RepoProfile{
		Confidence: make(map[string]float64),
	}

	// Layer 1: file existence checks
	detectProfileByFiles(root, p)

	// Layer 2: manifest parsing
	detectProfileByManifests(root, p)

	// Layer 3: monorepo polyglot scan
	if p.HasMonorepo {
		detectProfilePolyglot(root, p)
	}

	// Deduplicate all fields
	dedupProfile(p)

	return p, nil
}

// --- Layer 1: File existence checks ---

func detectProfileByFiles(root string, p *RepoProfile) {
	type fileRule struct {
		Path       string
		Set        func(*RepoProfile)
		Confidence float64
	}
	rules := []fileRule{
		// Languages
		{"go.mod", func(p *RepoProfile) { p.Languages = append(p.Languages, "go") }, 0.99},
		{"Cargo.toml", func(p *RepoProfile) { p.Languages = append(p.Languages, "rust") }, 0.99},
		{"package.json", func(p *RepoProfile) { p.Languages = append(p.Languages, "typescript", "javascript") }, 0.95},
		{"pyproject.toml", func(p *RepoProfile) { p.Languages = append(p.Languages, "python") }, 0.95},
		{"requirements.txt", func(p *RepoProfile) { p.Languages = append(p.Languages, "python") }, 0.90},
		{"Gemfile", func(p *RepoProfile) { p.Languages = append(p.Languages, "ruby") }, 0.99},
		{"composer.json", func(p *RepoProfile) { p.Languages = append(p.Languages, "php") }, 0.99},
		{"pom.xml", func(p *RepoProfile) { p.Languages = append(p.Languages, "java") }, 0.99},
		{"build.gradle", func(p *RepoProfile) { p.Languages = append(p.Languages, "java", "kotlin") }, 0.95},
		{"build.gradle.kts", func(p *RepoProfile) { p.Languages = append(p.Languages, "kotlin") }, 0.99},
		{"mix.exs", func(p *RepoProfile) { p.Languages = append(p.Languages, "elixir") }, 0.99},
		{"deno.json", func(p *RepoProfile) {
			p.Languages = append(p.Languages, "typescript")
			p.Frameworks = append(p.Frameworks, "deno")
		}, 0.99},
		{"deno.jsonc", func(p *RepoProfile) {
			p.Languages = append(p.Languages, "typescript")
			p.Frameworks = append(p.Frameworks, "deno")
		}, 0.99},

		// Build tools / monorepo
		{"turbo.json", func(p *RepoProfile) {
			p.BuildTools = append(p.BuildTools, "turborepo")
			p.HasMonorepo = true
		}, 0.99},
		{"nx.json", func(p *RepoProfile) {
			p.BuildTools = append(p.BuildTools, "nx")
			p.HasMonorepo = true
		}, 0.99},
		{"pnpm-workspace.yaml", func(p *RepoProfile) {
			p.PackageManagers = append(p.PackageManagers, "pnpm")
			p.HasMonorepo = true
		}, 0.99},
		{"lerna.json", func(p *RepoProfile) {
			p.BuildTools = append(p.BuildTools, "lerna")
			p.HasMonorepo = true
		}, 0.99},
		{"WORKSPACE", func(p *RepoProfile) {
			p.BuildTools = append(p.BuildTools, "bazel")
			p.HasMonorepo = true
		}, 0.99},
		{"WORKSPACE.bazel", func(p *RepoProfile) {
			p.BuildTools = append(p.BuildTools, "bazel")
			p.HasMonorepo = true
		}, 0.99},
		{"MODULE.bazel", func(p *RepoProfile) {
			p.BuildTools = append(p.BuildTools, "bazel")
			p.HasMonorepo = true
		}, 0.99},

		// Cloud providers
		{"wrangler.toml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "cloudflare") }, 0.99},
		{"wrangler.json", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "cloudflare") }, 0.99},
		{"fly.toml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "fly") }, 0.99},
		{"cloudbuild.yaml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp") }, 0.99},
		{".gcloudignore", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp") }, 0.95},
		{"firebase.json", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp", "firebase") }, 0.99},
		{".firebaserc", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp", "firebase") }, 0.99},
		{"vercel.json", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "vercel") }, 0.99},
		{"netlify.toml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "netlify") }, 0.99},
		{"render.yaml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "render") }, 0.99},
		{"serverless.yml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "aws") }, 0.85},
		{"cdk.json", func(p *RepoProfile) {
			p.CloudProviders = append(p.CloudProviders, "aws")
			p.InfraTools = append(p.InfraTools, "cdk")
		}, 0.99},

		// Container/orchestration
		{"Dockerfile", func(p *RepoProfile) {
			p.HasDocker = true
			p.InfraTools = append(p.InfraTools, "docker")
		}, 0.99},
		{"docker-compose.yml", func(p *RepoProfile) {
			p.HasDocker = true
			p.InfraTools = append(p.InfraTools, "docker", "docker-compose")
		}, 0.99},
		{"docker-compose.yaml", func(p *RepoProfile) {
			p.HasDocker = true
			p.InfraTools = append(p.InfraTools, "docker", "docker-compose")
		}, 0.99},
		{"compose.yml", func(p *RepoProfile) {
			p.HasDocker = true
			p.InfraTools = append(p.InfraTools, "docker", "docker-compose")
		}, 0.99},

		// CI/CD platforms
		{".github/workflows", func(p *RepoProfile) {
			p.CIPlatforms = append(p.CIPlatforms, "github-actions")
			p.HasCI = true
		}, 0.99},
		{".gitlab-ci.yml", func(p *RepoProfile) {
			p.CIPlatforms = append(p.CIPlatforms, "gitlab-ci")
			p.HasCI = true
		}, 0.99},
		{"Jenkinsfile", func(p *RepoProfile) {
			p.CIPlatforms = append(p.CIPlatforms, "jenkins")
			p.HasCI = true
		}, 0.99},
		{".circleci/config.yml", func(p *RepoProfile) {
			p.CIPlatforms = append(p.CIPlatforms, "circleci")
			p.HasCI = true
		}, 0.99},
		{".travis.yml", func(p *RepoProfile) {
			p.CIPlatforms = append(p.CIPlatforms, "travis")
			p.HasCI = true
		}, 0.99},
		{"azure-pipelines.yml", func(p *RepoProfile) {
			p.CIPlatforms = append(p.CIPlatforms, "azure-devops")
			p.HasCI = true
		}, 0.99},
	}

	for _, rule := range rules {
		if profileExists(filepath.Join(root, rule.Path)) {
			rule.Set(p)
			p.Confidence[rule.Path] = rule.Confidence
		}
	}
}

// --- Layer 2: Manifest parsing ---

func detectProfileByManifests(root string, p *RepoProfile) {
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		parseProfilePackageJSON(data, p)
	}
	if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		parseProfileGoMod(data, p)
	}
	if data, err := os.ReadFile(filepath.Join(root, "Cargo.toml")); err == nil {
		parseProfileCargoToml(data, p)
	}
	if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
		parseProfilePyDeps(string(data), p)
		if strings.Contains(string(data), "[tool.poetry]") {
			p.PackageManagers = append(p.PackageManagers, "poetry")
		}
		if strings.Contains(string(data), "[tool.uv]") {
			p.PackageManagers = append(p.PackageManagers, "uv")
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "requirements.txt")); err == nil {
		parseProfilePyDeps(string(data), p)
		p.PackageManagers = append(p.PackageManagers, "pip")
	}
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			parseProfileCompose(data, p)
			break
		}
	}
	for _, name := range []string{".env.example", ".env.template", ".env.local", ".env"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			parseProfileEnvFile(data, p)
			break
		}
	}
	detectProfileTerraform(root, p)
}

func parseProfilePackageJSON(data []byte, p *RepoProfile) {
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		Workspaces      json.RawMessage   `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return
	}
	if len(pkg.Workspaces) > 0 {
		p.HasMonorepo = true
	}
	all := map[string]bool{}
	for k := range pkg.Dependencies {
		all[k] = true
	}
	for k := range pkg.DevDependencies {
		all[k] = true
	}
	for dep := range all {
		switch {
		// Frameworks
		case dep == "next":
			p.Frameworks = append(p.Frameworks, "nextjs")
		case dep == "react":
			p.Frameworks = append(p.Frameworks, "react")
		case dep == "react-native":
			p.Frameworks = append(p.Frameworks, "react-native")
		case dep == "@nestjs/core":
			p.Frameworks = append(p.Frameworks, "nestjs")
		case dep == "fastify":
			p.Frameworks = append(p.Frameworks, "fastify")
		case dep == "express":
			p.Frameworks = append(p.Frameworks, "express")
		case dep == "vite":
			p.Frameworks = append(p.Frameworks, "vite")
			p.BuildTools = append(p.BuildTools, "vite")
		case dep == "vue":
			p.Frameworks = append(p.Frameworks, "vue")
		case dep == "@sveltejs/kit":
			p.Frameworks = append(p.Frameworks, "sveltekit")
		case dep == "astro":
			p.Frameworks = append(p.Frameworks, "astro")
		case dep == "nuxt":
			p.Frameworks = append(p.Frameworks, "nuxt")
		case dep == "@remix-run/dev":
			p.Frameworks = append(p.Frameworks, "remix")
		case dep == "@angular/cli":
			p.Frameworks = append(p.Frameworks, "angular")
		case dep == "hono":
			p.Frameworks = append(p.Frameworks, "hono")
		case dep == "expo":
			p.Frameworks = append(p.Frameworks, "expo", "react-native")
		// Databases
		case dep == "pg" || dep == "pg-pool":
			p.Databases = append(p.Databases, "postgres")
		case dep == "mysql2":
			p.Databases = append(p.Databases, "mysql")
		case dep == "mongoose" || dep == "mongodb":
			p.Databases = append(p.Databases, "mongo")
		case dep == "redis" || dep == "ioredis":
			p.Databases = append(p.Databases, "redis")
		case dep == "better-sqlite3" || dep == "sqlite3":
			p.Databases = append(p.Databases, "sqlite")
		case dep == "prisma" || dep == "@prisma/client":
			p.Frameworks = append(p.Frameworks, "prisma")
		case dep == "drizzle-orm":
			p.Frameworks = append(p.Frameworks, "drizzle")
		case dep == "@supabase/supabase-js":
			p.CloudProviders = append(p.CloudProviders, "supabase")
			p.Databases = append(p.Databases, "postgres")
		// Message queues
		case dep == "kafkajs" || strings.HasPrefix(dep, "@confluentinc/"):
			p.MessageQueues = append(p.MessageQueues, "kafka")
		case dep == "amqplib":
			p.MessageQueues = append(p.MessageQueues, "rabbitmq")
		case dep == "bullmq" || dep == "bull":
			p.MessageQueues = append(p.MessageQueues, "bullmq")
		// Cloud SDKs
		case strings.HasPrefix(dep, "@aws-sdk/") || dep == "aws-sdk":
			p.CloudProviders = append(p.CloudProviders, "aws")
		case strings.HasPrefix(dep, "@google-cloud/") || dep == "firebase-admin":
			p.CloudProviders = append(p.CloudProviders, "gcp")
		case strings.HasPrefix(dep, "@azure/"):
			p.CloudProviders = append(p.CloudProviders, "azure")
		case strings.HasPrefix(dep, "@cloudflare/"):
			p.CloudProviders = append(p.CloudProviders, "cloudflare")
		// Protocols
		case dep == "graphql" || strings.HasPrefix(dep, "@apollo/"):
			p.Protocols = append(p.Protocols, "graphql")
		case strings.HasPrefix(dep, "@grpc/"):
			p.Protocols = append(p.Protocols, "grpc")
		case dep == "ws" || dep == "socket.io" || dep == "socket.io-client":
			p.Protocols = append(p.Protocols, "websocket")
		case strings.HasPrefix(dep, "@modelcontextprotocol/"):
			p.Protocols = append(p.Protocols, "mcp")
		// Test frameworks
		case dep == "jest" || dep == "@jest/core":
			p.TestFrameworks = append(p.TestFrameworks, "jest")
		case dep == "vitest":
			p.TestFrameworks = append(p.TestFrameworks, "vitest")
		case dep == "@playwright/test":
			p.TestFrameworks = append(p.TestFrameworks, "playwright")
		case dep == "cypress":
			p.TestFrameworks = append(p.TestFrameworks, "cypress")
		case dep == "stripe":
			p.Frameworks = append(p.Frameworks, "stripe")
		}
	}
}

func parseProfileGoMod(data []byte, p *RepoProfile) {
	content := string(data)
	type rule struct {
		Pattern string
		Apply   func(*RepoProfile)
	}
	rules := []rule{
		{"github.com/jackc/pgx", func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
		{"github.com/lib/pq", func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
		{"gorm.io/driver/postgres", func(p *RepoProfile) {
			p.Databases = append(p.Databases, "postgres")
			p.Frameworks = append(p.Frameworks, "gorm")
		}},
		{"github.com/go-sql-driver/mysql", func(p *RepoProfile) { p.Databases = append(p.Databases, "mysql") }},
		{"go.mongodb.org/mongo-driver", func(p *RepoProfile) { p.Databases = append(p.Databases, "mongo") }},
		{"github.com/redis/go-redis", func(p *RepoProfile) { p.Databases = append(p.Databases, "redis") }},
		{"github.com/go-redis/redis", func(p *RepoProfile) { p.Databases = append(p.Databases, "redis") }},
		{"github.com/mattn/go-sqlite3", func(p *RepoProfile) { p.Databases = append(p.Databases, "sqlite") }},
		{"modernc.org/sqlite", func(p *RepoProfile) { p.Databases = append(p.Databases, "sqlite") }},
		{"github.com/twmb/franz-go", func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "kafka") }},
		{"github.com/segmentio/kafka-go", func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "kafka") }},
		{"google.golang.org/grpc", func(p *RepoProfile) { p.Protocols = append(p.Protocols, "grpc") }},
		{"github.com/99designs/gqlgen", func(p *RepoProfile) { p.Protocols = append(p.Protocols, "graphql") }},
		{"github.com/aws/aws-sdk-go-v2", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "aws") }},
		{"github.com/aws/aws-sdk-go", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "aws") }},
		{"cloud.google.com/go", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp") }},
		{"github.com/Azure/azure-sdk-for-go", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "azure") }},
		{"github.com/gin-gonic/gin", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "gin") }},
		{"github.com/labstack/echo", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "echo") }},
		{"github.com/gofiber/fiber", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "fiber") }},
		{"github.com/go-chi/chi", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "chi") }},
		{"github.com/stripe/stripe-go", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "stripe") }},
	}
	for _, r := range rules {
		if strings.Contains(content, r.Pattern) {
			r.Apply(p)
		}
	}
}

func parseProfileCargoToml(data []byte, p *RepoProfile) {
	content := string(data)
	type rule struct {
		Pattern string
		Apply   func(*RepoProfile)
	}
	rules := []rule{
		{"tokio-postgres", func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
		{"mongodb", func(p *RepoProfile) { p.Databases = append(p.Databases, "mongo") }},
		{"axum", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "axum") }},
		{"actix-web", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "actix") }},
		{"rocket", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "rocket") }},
		{"tokio", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "tokio") }},
	}
	for _, r := range rules {
		if strings.Contains(content, r.Pattern) {
			r.Apply(p)
		}
	}
	if strings.Contains(content, "[workspace]") {
		p.HasMonorepo = true
	}
}

func parseProfilePyDeps(content string, p *RepoProfile) {
	lower := strings.ToLower(content)
	type rule struct {
		Pattern string
		Apply   func(*RepoProfile)
	}
	rules := []rule{
		{"psycopg2", func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
		{"asyncpg", func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
		{"pymongo", func(p *RepoProfile) { p.Databases = append(p.Databases, "mongo") }},
		{"redis", func(p *RepoProfile) { p.Databases = append(p.Databases, "redis") }},
		{"django", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "django") }},
		{"flask", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "flask") }},
		{"fastapi", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "fastapi") }},
		{"pytest", func(p *RepoProfile) { p.TestFrameworks = append(p.TestFrameworks, "pytest") }},
		{"sqlalchemy", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "sqlalchemy") }},
		{"boto3", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "aws") }},
		{"google-cloud-", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp") }},
	}
	for _, r := range rules {
		if strings.Contains(lower, r.Pattern) {
			r.Apply(p)
		}
	}
}

var composePatterns = []struct {
	Pattern *regexp.Regexp
	Apply   func(*RepoProfile)
}{
	{regexp.MustCompile(`(?m)image:\s*(?:postgres|postgresql)(?::|\s|$)`), func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
	{regexp.MustCompile(`(?m)image:\s*mysql(?::|\s|$)`), func(p *RepoProfile) { p.Databases = append(p.Databases, "mysql") }},
	{regexp.MustCompile(`(?m)image:\s*mongo(?:db)?(?::|\s|$)`), func(p *RepoProfile) { p.Databases = append(p.Databases, "mongo") }},
	{regexp.MustCompile(`(?m)image:\s*redis(?::|\s|$)`), func(p *RepoProfile) { p.Databases = append(p.Databases, "redis") }},
	{regexp.MustCompile(`(?m)image:\s*valkey(?::|\s|$)`), func(p *RepoProfile) { p.Databases = append(p.Databases, "valkey") }},
	{regexp.MustCompile(`elasticsearch`), func(p *RepoProfile) { p.Databases = append(p.Databases, "elasticsearch") }},
	{regexp.MustCompile(`(?m)image:\s*(?:apache/)?kafka`), func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "kafka") }},
	{regexp.MustCompile(`(?m)image:\s*confluentinc/cp-kafka`), func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "kafka") }},
	{regexp.MustCompile(`(?m)image:\s*rabbitmq`), func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "rabbitmq") }},
	{regexp.MustCompile(`(?m)image:\s*nats`), func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "nats") }},
}

func parseProfileCompose(data []byte, p *RepoProfile) {
	content := string(data)
	for _, rule := range composePatterns {
		if rule.Pattern.MatchString(content) {
			rule.Apply(p)
		}
	}
}

func parseProfileEnvFile(data []byte, p *RepoProfile) {
	content := string(data)
	if strings.Contains(content, "DATABASE_URL=postgres") || strings.Contains(content, "DATABASE_URL=postgresql") {
		p.Databases = append(p.Databases, "postgres")
	}
	if strings.Contains(content, "DATABASE_URL=mysql") {
		p.Databases = append(p.Databases, "mysql")
	}
	if strings.Contains(content, "MONGODB_URI") || strings.Contains(content, "MONGO_URL") {
		p.Databases = append(p.Databases, "mongo")
	}
	if strings.Contains(content, "REDIS_URL") || strings.Contains(content, "REDIS_HOST") {
		p.Databases = append(p.Databases, "redis")
	}
	if strings.Contains(content, "KAFKA_BROKERS") || strings.Contains(content, "KAFKA_BOOTSTRAP_SERVERS") {
		p.MessageQueues = append(p.MessageQueues, "kafka")
	}
	if strings.Contains(content, "AWS_ACCESS_KEY_ID") || strings.Contains(content, "AWS_REGION") {
		p.CloudProviders = append(p.CloudProviders, "aws")
	}
	if strings.Contains(content, "GOOGLE_CLOUD_PROJECT") || strings.Contains(content, "GCP_PROJECT") {
		p.CloudProviders = append(p.CloudProviders, "gcp")
	}
}

func detectProfileTerraform(root string, p *RepoProfile) {
	found := false
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".tf") {
			found = true
			if data, err := os.ReadFile(path); err == nil {
				content := string(data)
				if strings.Contains(content, `"aws"`) || strings.Contains(content, "hashicorp/aws") {
					p.CloudProviders = append(p.CloudProviders, "aws")
				}
				if strings.Contains(content, `"google"`) || strings.Contains(content, "hashicorp/google") {
					p.CloudProviders = append(p.CloudProviders, "gcp")
				}
				if strings.Contains(content, `"azurerm"`) {
					p.CloudProviders = append(p.CloudProviders, "azure")
				}
				if strings.Contains(content, `"cloudflare"`) {
					p.CloudProviders = append(p.CloudProviders, "cloudflare")
				}
			}
		}
		return nil
	})
	if found {
		p.InfraTools = append(p.InfraTools, "terraform")
	}
}

func detectProfilePolyglot(root string, p *RepoProfile) {
	dirs := []string{"apps", "packages", "services", "libs", "modules", "tools"}
	for _, d := range dirs {
		sub := filepath.Join(root, d)
		if info, err := os.Stat(sub); err == nil && info.IsDir() {
			entries, _ := os.ReadDir(sub)
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				subRoot := filepath.Join(sub, entry.Name())
				detectProfileByFiles(subRoot, p)
				detectProfileByManifests(subRoot, p)
			}
		}
	}
}

func profileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func dedupProfile(p *RepoProfile) {
	p.Languages = dedupProfileStrings(p.Languages)
	p.Frameworks = dedupProfileStrings(p.Frameworks)
	p.Databases = dedupProfileStrings(p.Databases)
	p.MessageQueues = dedupProfileStrings(p.MessageQueues)
	p.CloudProviders = dedupProfileStrings(p.CloudProviders)
	p.InfraTools = dedupProfileStrings(p.InfraTools)
	p.Protocols = dedupProfileStrings(p.Protocols)
	p.BuildTools = dedupProfileStrings(p.BuildTools)
	p.PackageManagers = dedupProfileStrings(p.PackageManagers)
	p.TestFrameworks = dedupProfileStrings(p.TestFrameworks)
	p.CIPlatforms = dedupProfileStrings(p.CIPlatforms)
}

func dedupProfileStrings(s []string) []string {
	seen := make(map[string]bool, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
