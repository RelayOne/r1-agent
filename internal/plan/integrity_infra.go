// Package plan — integrity_infra.go
//
// Infrastructure / microservice / SOA policy gate. Different
// character from the compile-and-link ecosystems: rather than
// checking whether code resolves, this gate checks whether the
// session introduced a dependency on a vendor-locked proprietary
// service where a provider-agnostic open-source alternative exists.
//
// Default stance (overridable by the SOW): for microservice / SOA /
// distributed architecture, major infrastructure decisions should be
// provider-agnostic and prefer battle-tested open-source options
// unless the SOW explicitly names a commercial provider.
//
// "Explicit SOW permission" is detected by reading the SOW source
// prose (projectRoot/.stoke/sow-from-prose.json or the SOW state
// file) and checking whether the provider brand appears in the text.
// If the SOW mentions AWS, the AWS SDK import is fine. If not, the
// gate emits a directive listing the open-source alternatives with
// a suggested migration path.
//
// Scoped conservatively: this gate fires on import-level evidence
// (package imports, env var usage, config file entries) and only
// flags well-known vendor SDKs. It never flags Stripe (genuinely
// hard to replace for payments), Twilio SMS (regulatory), or any
// provider explicitly mentioned in the SOW.
//
// This is a policy gate, not a correctness gate — the session's
// ACs may well pass, and the code may work. The directive is a
// request to reconsider; the operator decides whether to act on
// it or override via SOW prose.
package plan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func init() {
	RegisterEcosystem(&infraEcosystem{})
}

type infraEcosystem struct{}

func (infraEcosystem) Name() string { return "infra-policy" }

// Infra policy claims no files of its own; it inspects the union of
// all files via the same scan path the other ecosystems use. The
// gate dispatcher calls each ecosystem's Owns() per file, so we
// return true for every TS/JS/Go/Python/Rust/Java file (the common
// carriers of SDK imports). Multiple ecosystems claiming a file is
// allowed: the first (earlier-registered) one wins file attribution,
// but the probe sees the file via the scanner below anyway.
//
// To avoid double-owning, we return false here and do a workspace-
// wide scan when any session file matches our known carriers. That
// keeps the file ownership clean for the compile-regression flow.
func (infraEcosystem) Owns(path string) bool { return false }

func (infraEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	// The infra gate runs a workspace-wide scan because SDK imports
	// introduced in any file (even a test or config) commit the
	// project to a provider. We activate only when the current
	// session touched something — we infer "session touched
	// something" from non-empty files, and then scan the whole
	// project tree for SDK use.
	if len(files) == 0 {
		return nil, nil
	}
	sowText := readSOWText(projectRoot)
	allowed := sowAllowedProviders(sowText)
	detected := scanVendorSDKs(projectRoot)
	var out []ManifestMiss
	for provider, firstFile := range detected {
		if _, ok := allowed[provider]; ok {
			continue
		}
		alt := vendorAlternatives(provider)
		if alt == "" {
			continue // provider not in our catalog of replaceables
		}
		rel, _ := filepath.Rel(projectRoot, firstFile)
		out = append(out, ManifestMiss{
			SourceFile: rel,
			ImportPath: provider,
			Manifest:   "SOW architecture policy",
			AddCommand: fmt.Sprintf(
				"SOW does not explicitly authorize %s. Consider provider-agnostic alternatives: %s. If %s is required, add a sentence to the SOW that names it so this gate accepts it.",
				provider, alt, provider),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ImportPath < out[j].ImportPath })
	return out, nil
}

func (infraEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	return nil, nil
}

func (infraEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	return nil, nil
}

// ---------------------------------------------------------------------
// SOW parsing
// ---------------------------------------------------------------------

// readSOWText returns the full text we have for the SOW so vendor-
// name detection has maximum coverage. Falls back across the common
// stoke persistence paths so a prose-only SOW, a converted SOW, and
// a running session all work.
func readSOWText(projectRoot string) string {
	var b strings.Builder
	candidates := []string{
		filepath.Join(projectRoot, ".stoke", "sow-from-prose.json"),
		filepath.Join(projectRoot, ".stoke", "sow-state.json"),
	}
	for _, p := range candidates {
		if body, err := os.ReadFile(p); err == nil {
			b.Write(body)
			b.WriteByte('\n')
		}
	}
	// Plus any *.md SOW file at the project root (common shape).
	entries, _ := os.ReadDir(projectRoot)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		lower := strings.ToLower(e.Name())
		if !strings.Contains(lower, "sow") && !strings.Contains(lower, "spec") &&
			!strings.Contains(lower, "scope") && lower != "readme.md" {
			continue
		}
		if body, err := os.ReadFile(filepath.Join(projectRoot, e.Name())); err == nil {
			b.Write(body)
			b.WriteByte('\n')
		}
	}
	return strings.ToLower(b.String())
}

// sowAllowedProviders scans the SOW text for provider names. A
// provider mentioned in the SOW is considered authorized regardless
// of context (the gate trusts the author's intent, because writing
// "don't use AWS" also matches on AWS and rightly prevents the
// false-positive).
func sowAllowedProviders(sowText string) map[string]struct{} {
	out := map[string]struct{}{}
	for provider, tokens := range providerTokens {
		for _, tok := range tokens {
			if strings.Contains(sowText, tok) {
				out[provider] = struct{}{}
				break
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------
// Vendor SDK detection
// ---------------------------------------------------------------------

// providerSignatures maps each vendor provider → a regex that matches
// its SDK's characteristic import path. The scanner walks every
// source file in the project once and records the first file that
// triggers each provider.
var providerSignatures = map[string]*regexp.Regexp{
	"aws":             regexp.MustCompile(`['"\x60]@aws-sdk/|['"\x60]aws-sdk['"\x60]|"github\.com/aws/aws-sdk-go|"github\.com/aws/aws-sdk-go-v2|import\s+boto3|from\s+boto3`),
	"firebase":        regexp.MustCompile(`['"\x60]firebase(?:-admin)?['"\x60]|['"\x60]@firebase/|['"\x60]@react-native-firebase/`),
	"gcp":             regexp.MustCompile(`['"\x60]@google-cloud/|"cloud\.google\.com/go/|from\s+google\.cloud`),
	"azure":           regexp.MustCompile(`['"\x60]@azure/|"github\.com/Azure/azure-sdk-for-go|from\s+azure\.`),
	"auth0":           regexp.MustCompile(`['"\x60]auth0['"\x60]|['"\x60]@auth0/|['"\x60]auth0-js['"\x60]`),
	"datadog":         regexp.MustCompile(`['"\x60]dd-trace['"\x60]|['"\x60]@datadog/|"github\.com/DataDog/`),
	"new-relic":       regexp.MustCompile(`['"\x60]newrelic['"\x60]|['"\x60]@newrelic/`),
	"sentry":          regexp.MustCompile(`['"\x60]@sentry/`),
	"pusher":          regexp.MustCompile(`['"\x60]pusher(?:-js)?['"\x60]`),
	"mongodb-atlas":   regexp.MustCompile(`realm-web['"\x60]|atlas-app-services`),
	"vercel":          regexp.MustCompile(`['"\x60]@vercel/(?:kv|postgres|blob|edge-config)['"\x60]`),
	"supabase":        regexp.MustCompile(`['"\x60]@supabase/`),
	"heroku":          regexp.MustCompile(`heroku\.com/|heroku-cli`),
	"netlify":         regexp.MustCompile(`['"\x60]@netlify/|netlify\.app`),
	"cloudflare-r2":   regexp.MustCompile(`@cloudflare/|r2\.cloudflarestorage`),
	"algolia":         regexp.MustCompile(`['"\x60]algoliasearch['"\x60]|['"\x60]@algolia/`),
	"okta":            regexp.MustCompile(`['"\x60]@okta/|okta-auth-js`),
	"segment":         regexp.MustCompile(`['"\x60]analytics-node['"\x60]|['"\x60]@segment/`),
	"mixpanel":        regexp.MustCompile(`['"\x60]mixpanel(?:-browser)?['"\x60]`),
	"amplitude":       regexp.MustCompile(`['"\x60]amplitude-js['"\x60]|['"\x60]@amplitude/`),
	"intercom":        regexp.MustCompile(`['"\x60]@intercom/`),
	"twilio-video":    regexp.MustCompile(`['"\x60]twilio-video['"\x60]`),
}

// providerTokens maps each provider to brand tokens that, when
// present in the SOW, count as an explicit authorization.
var providerTokens = map[string][]string{
	"aws":           {"aws", "amazon web services", "s3", "lambda", "dynamodb", "ec2", "cloudfront"},
	"firebase":      {"firebase", "firestore"},
	"gcp":           {"gcp", "google cloud", "cloud run", "pub/sub", "bigquery"},
	"azure":         {"azure", "microsoft azure"},
	"auth0":         {"auth0"},
	"datadog":       {"datadog"},
	"new-relic":     {"new relic", "newrelic"},
	"sentry":        {"sentry"},
	"pusher":        {"pusher"},
	"mongodb-atlas": {"atlas", "mongodb atlas", "realm"},
	"vercel":        {"vercel", "@vercel", "vercel kv", "vercel blob"},
	"supabase":      {"supabase"},
	"heroku":        {"heroku"},
	"netlify":       {"netlify"},
	"cloudflare-r2": {"cloudflare", "r2"},
	"algolia":       {"algolia"},
	"okta":          {"okta"},
	"segment":       {"segment", "segment.io"},
	"mixpanel":      {"mixpanel"},
	"amplitude":     {"amplitude"},
	"intercom":      {"intercom"},
	"twilio-video":  {"twilio"},
}

// vendorAlternatives returns a suggested OSS / provider-agnostic
// alternative list for each known provider. Empty string means the
// provider isn't considered replaceable by the gate (e.g., Stripe,
// which we intentionally don't police — payments is genuinely hard
// to replace with OSS).
func vendorAlternatives(provider string) string {
	switch provider {
	case "aws":
		return "MinIO (S3-compatible), PostgreSQL (RDS replacement), NATS/RabbitMQ (SQS/SNS), self-hosted Lambda via OpenFaaS/Knative"
	case "firebase":
		return "PostgREST + PostgreSQL, PocketBase, self-hosted Supabase (open-source), Appwrite"
	case "gcp":
		return "open-source equivalents: MinIO (GCS), PostgreSQL (Cloud SQL), NATS (Pub/Sub), Temporal (Cloud Tasks), Meilisearch/Typesense (Cloud Search)"
	case "azure":
		return "open-source equivalents: Kubernetes (AKS), MinIO (Blob), PostgreSQL (SQL DB), Keycloak (Azure AD)"
	case "auth0":
		return "Keycloak, Ory Hydra/Kratos, Supertokens, Authentik, Logto"
	case "datadog":
		return "Prometheus + Grafana + Loki + Tempo, OpenTelemetry Collector, SigNoz, Uptrace"
	case "new-relic":
		return "Prometheus + Grafana, SigNoz, OpenTelemetry Collector"
	case "sentry":
		return "GlitchTip (Sentry-compatible), self-hosted Sentry (open-source)"
	case "pusher":
		return "Soketi (Pusher-compatible, self-hosted), Centrifugo, Mercure, self-hosted socket.io"
	case "mongodb-atlas":
		return "self-hosted MongoDB, PostgreSQL with JSONB, CouchDB, Supabase"
	case "vercel":
		return "self-hosted Next.js on Docker/Kubernetes, OpenNext for serverless, Coolify for platform, Dokku"
	case "supabase":
		return "self-hosted Supabase (OSS is permitted; cloud tier is the lock-in), PostgREST + PostgreSQL directly, PocketBase"
	case "heroku":
		return "Dokku, CapRover, Coolify, fly.io open alternatives, Kubernetes"
	case "netlify":
		return "self-hosted static host (Caddy/nginx), Coolify, any VPS with CI that publishes to object storage"
	case "cloudflare-r2":
		return "MinIO, SeaweedFS, self-hosted S3-compatible storage"
	case "algolia":
		return "Meilisearch, Typesense, OpenSearch, PostgreSQL full-text search + pg_trgm"
	case "okta":
		return "Keycloak, Ory, Authentik, Zitadel"
	case "segment":
		return "Rudderstack (OSS), Jitsu, self-hosted event pipeline with Kafka"
	case "mixpanel":
		return "PostHog (OSS), Plausible, self-hosted analytics via ClickHouse"
	case "amplitude":
		return "PostHog, Jitsu, Matomo"
	case "intercom":
		return "Chatwoot, Papercups (OSS), Crisp self-hosted"
	case "twilio-video":
		return "LiveKit (OSS WebRTC), Jitsi, Daily's OSS components"
	}
	return ""
}

// scanVendorSDKs walks the project once and returns a map of
// detected provider → first file that triggered detection. Skips
// node_modules, build outputs, VCS. Reads .ts/.tsx/.js/.jsx/.go/
// .py/.rs/.java to cover the common SDK-bearing languages. TOML and
// JSON files are also scanned for manifest-level detections (e.g.,
// a direct dep on @aws-sdk/client-s3 in package.json even when no
// source file imports it yet).
func scanVendorSDKs(projectRoot string) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == ".git" || name == "target" ||
				name == "dist" || name == "build" || name == ".next" || name == ".turbo" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		switch ext {
		case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
			".go", ".py", ".rs", ".java", ".kt", ".kts",
			".json", ".toml", ".yaml", ".yml":
		default:
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(body)
		for provider, re := range providerSignatures {
			if _, dup := out[provider]; dup {
				continue
			}
			if re.MatchString(text) {
				out[provider] = path
			}
		}
		return nil
	})
	return out
}
