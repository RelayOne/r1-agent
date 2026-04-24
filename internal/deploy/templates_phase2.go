package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/atomicfs"
)

// Template bodies pulled verbatim from specs/deploy-phase2.md §Config File Templates.
// Do not paraphrase. Substitution tokens use the {{TOKEN}} form and are filled
// in from the params map by Render.

// vercelNextJSONTemplate is the minimal Next.js vercel.json described in the spec.
const vercelNextJSONTemplate = `{
  "$schema": "https://openapi.vercel.sh/vercel.json",
  "version": 2,
  "framework": "nextjs",
  "regions": ["iad1"],
  "env": {
    "NODE_ENV": "production"
  }
}
`

// vercelStaticJSONTemplate is the plain Node/Express serverless vercel.json.
// The spec labels this "plain Node/Express as serverless"; the task checklist
// maps ("vercel", "static") to it as the non-Next.js vercel.json shape.
const vercelStaticJSONTemplate = `{
  "$schema": "https://openapi.vercel.sh/vercel.json",
  "version": 2,
  "builds": [
    { "src": "server.js", "use": "@vercel/node" }
  ],
  "routes": [
    { "src": "/(.*)", "dest": "/server.js" }
  ]
}
`

// wranglerWorkersTOMLTemplate is the Workers + static assets wrangler.toml.
const wranglerWorkersTOMLTemplate = `name = "{{NAME}}"
main = "src/index.js"
compatibility_date = "{{DATE}}"   # default: today's date at generation time
compatibility_flags = ["nodejs_compat"]

[assets]
  directory = "./public"
  binding = "ASSETS"

# Env-var docs:
#   CLOUDFLARE_API_TOKEN   — deploy auth; scopes: Workers Scripts:Edit,
#                            Account Settings:Read, User Details:Read
#   CLOUDFLARE_ACCOUNT_ID  — target account
#   WRANGLER_OUTPUT_FILE_PATH — set by Stoke; do not override
`

// wranglerPagesTOMLTemplate is the pure Worker (no static assets) wrangler.toml.
// Per the spec, legacy Pages uses "content stripped of [assets] block because
// Pages uses a different directory convention." The ("cloudflare", "pages")
// pair maps to this minimal, assets-free form.
const wranglerPagesTOMLTemplate = `name = "{{NAME}}"
main = "src/index.js"
compatibility_date = "{{DATE}}"
`

// templateEntry pairs a template body with its suggested output path.
type templateEntry struct {
	path string
	body string
}

// templates indexes supported (provider, stack) pairs.
var templates = map[string]map[string]templateEntry{
	"vercel": {
		"next":   {path: "vercel.json", body: vercelNextJSONTemplate},
		"static": {path: "vercel.json", body: vercelStaticJSONTemplate},
	},
	"cloudflare": {
		"workers": {path: "wrangler.toml", body: wranglerWorkersTOMLTemplate},
		"pages":   {path: "wrangler.toml", body: wranglerPagesTOMLTemplate},
	},
}

// tokenRE matches {{NAME}}-style substitution tokens. Token names must be
// uppercase ASCII letters, digits, or underscore with a leading letter.
var tokenRE = regexp.MustCompile(`\{\{([A-Z][A-Z0-9_]*)\}\}`)

// Render returns a suggested relative path and rendered content for the given
// (provider, stack) pair, substituting {{TOKEN}} tokens from params.
//
// Render does NOT write anything to disk. Callers that want to write should
// use WriteIfAbsent (or drive atomicfs directly) so existing files are never
// overwritten. Returns a descriptive error if the pair is unknown or any
// required token is missing from params. The single exception is DATE, which
// auto-defaults to today's UTC date per the wrangler.toml comment.
func Render(provider, stack string, params map[string]string) (path, content string, err error) {
	byStack, ok := templates[provider]
	if !ok {
		return "", "", fmt.Errorf("deploy: unknown provider %q (known: %s)", provider, knownProviders())
	}
	entry, ok := byStack[stack]
	if !ok {
		return "", "", fmt.Errorf("deploy: unknown stack %q for provider %q (known: %s)", stack, provider, knownStacks(provider))
	}

	missing := []string{}
	rendered := tokenRE.ReplaceAllStringFunc(entry.body, func(match string) string {
		key := match[2 : len(match)-2]
		if v, ok := params[key]; ok {
			return v
		}
		if key == "DATE" {
			// DATE auto-defaults to today's UTC date; every other token must
			// be supplied by the caller so operator intent is explicit.
			return time.Now().UTC().Format("2006-01-02")
		}
		missing = append(missing, key)
		return match
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		dedup := missing[:0]
		var last string
		for _, k := range missing {
			if k == last {
				continue
			}
			dedup = append(dedup, k)
			last = k
		}
		return "", "", fmt.Errorf("deploy: render %s/%s missing required params: %s", provider, stack, strings.Join(dedup, ", "))
	}
	return entry.path, rendered, nil
}

// WriteIfAbsent renders the template and writes it under dir only when the
// target path does not already exist. On existing file, returns wrote=false
// with no error and no modification. The write is transactional via atomicfs.
//
// The returned path is the dir-joined absolute-or-relative path that would be
// or was written; callers should log it regardless of wrote.
func WriteIfAbsent(dir, provider, stack string, params map[string]string) (path string, wrote bool, err error) {
	rel, content, err := Render(provider, stack, params)
	if err != nil {
		return "", false, err
	}
	full := filepath.Join(dir, rel)

	// No-overwrite: if the file exists, return wrote=false. atomicfs.Create
	// enforces the same rule at commit time, but short-circuiting here lets
	// the caller distinguish "already configured" from a real error.
	if _, statErr := os.Stat(full); statErr == nil {
		return full, false, nil
	} else if !os.IsNotExist(statErr) {
		return full, false, fmt.Errorf("deploy: stat %s: %w", full, statErr)
	}

	tx := atomicfs.NewTransaction(dir)
	if err := tx.Create(rel, []byte(content)); err != nil {
		return full, false, fmt.Errorf("deploy: stage %s: %w", full, err)
	}
	if err := tx.Commit(); err != nil {
		return full, false, fmt.Errorf("deploy: write %s: %w", full, err)
	}
	return full, true, nil
}

// knownProviders returns a sorted, comma-joined list of registered template
// providers for error messages.
func knownProviders() string {
	ks := make([]string, 0, len(templates))
	for k := range templates {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}

// knownStacks returns a sorted, comma-joined list of stacks registered for
// the given provider. Returns "" if the provider has no templates.
func knownStacks(provider string) string {
	byStack, ok := templates[provider]
	if !ok {
		return ""
	}
	ks := make([]string, 0, len(byStack))
	for k := range byStack {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}
