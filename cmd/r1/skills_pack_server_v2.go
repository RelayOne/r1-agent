package main

// skills_pack_server_v2.go — C7 cross-product-skill-exchange T2/T9/T6b.
//
// Federated v2 surface for the pack registry. Coexists with the v1
// handlers in skills_pack_server.go without modifying any v1 logic.
// Adds:
//
//   - /v2/packs                     list with optional ?compat= filter
//   - /v2/packs/{id}                detail (v2 manifest + envelope)
//   - /v2/packs/{id}/blob.tar.gz    archive (same shape as v1)
//   - /v2/packs/{id}/sig            detached pack.sig.json envelope
//   - /v2/packs/search              free-text + ?compat= + ?authority=
//   - /v2/trust-root                signed TrustRootDocument
//
// Cross-cutting concerns:
//   - Per-IP token-bucket rate limit (T9b) applies to ALL /v2/ routes.
//   - Response signature middleware (T9c) wraps every JSON response;
//     archive bytes are excluded as documented in the spec.
//   - HTTPS-only in production via --cert/--key flags (T9a). One-line
//     stderr warning if served plain HTTP.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/r1env"
	"github.com/RelayOne/r1/internal/skill"
	"github.com/RelayOne/r1/internal/skillmfr"
)

// defaultV2RateLimit is the per-IP token-bucket rate in requests/min
// when R1_PACK_REGISTRY_RATE_LIMIT is unset.
const defaultV2RateLimit = 60

// idleLimiterEvictAfter is the grace period after which an inactive
// per-IP limiter is removed from the sync.Map.
const idleLimiterEvictAfter = 10 * time.Minute

// v2RootKeyEnv names the env that supplies the path to the registry's
// root operator private key. The same key signs:
//   - the trust-root document
//   - every /v2/ JSON response (X-R1-Registry-Sig)
const v2RootKeyEnv = "R1_REGISTRY_ROOT_KEY"

// v2RateLimitEnv names the env for the per-IP rate-limit override.
const v2RateLimitEnv = "R1_PACK_REGISTRY_RATE_LIMIT"

// PackSummaryV2 extends the v1 summary with v2 fields. JSON field
// names use snake_case to match the rest of the registry API.
type PackSummaryV2 struct {
	Name                  string   `json:"name"`
	Version               string   `json:"version"`
	Description           string   `json:"description,omitempty"`
	ManifestSchemaVersion string   `json:"manifest_schema_version"`
	Compat                []string `json:"compat"`
	SignatureAuthority    string   `json:"signature_authority,omitempty"`
	Signed                bool     `json:"signed"`
	SignatureKeyID        string   `json:"signature_key_id,omitempty"`
	DownloadURLPath       string   `json:"download_url_path"`
	SigURLPath            string   `json:"sig_url_path"`
}

// PackDetailV2 is the /v2/packs/{id} response shape.
type PackDetailV2 struct {
	PackSummaryV2
	Dependencies      []string                    `json:"dependencies,omitempty"`
	RuntimeAssertions map[string][]string         `json:"runtime_assertions,omitempty"`
	ConsumerHooks     map[string]skill.HookSpec   `json:"consumer_hooks,omitempty"`
	MinR1Version      string                      `json:"min_r1_version,omitempty"`
	SourcePath        string                      `json:"source_path"`
	SignatureEnvelope *skillmfr.PackSignature     `json:"signature_envelope,omitempty"`
}

// PackSearchEntryV2 is one entry in /v2/packs/search response.
type PackSearchEntryV2 struct {
	PackSummaryV2
	MatchFields []string `json:"match_fields,omitempty"`
}

// v2Handler implements the /v2/ surface on top of skillPackRegistryServer.
// The same struct may serve both v1 and v2; we attach v2 state here so
// the v1 path stays unchanged.
type v2Handler struct {
	base        *skillPackRegistryServer
	rateLimit   int
	rootPriv    ed25519.PrivateKey
	rootPub     ed25519.PublicKey
	trustRoot   *skill.TrustRootDocument
	trustRootMu sync.Mutex
	trustRootAt time.Time
	limiters    sync.Map // string(ip) -> *ipLimiter
}

// ipLimiter is a tiny token-bucket. Avoids pulling in
// golang.org/x/time/rate (not vendored) while keeping the spec's
// "60 req/min per IP" contract.
type ipLimiter struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per nanosecond
	last       time.Time
}

func newIPLimiter(perMinute int) *ipLimiter {
	if perMinute <= 0 {
		perMinute = defaultV2RateLimit
	}
	max := float64(perMinute)
	// refill = perMinute tokens / 60s
	refill := max / float64(60*time.Second/time.Nanosecond)
	return &ipLimiter{
		tokens:     max,
		maxTokens:  max,
		refillRate: refill,
		last:       time.Now(),
	}
}

// allow consumes one token if available. Returns the suggested
// Retry-After seconds when it refuses.
func (l *ipLimiter) allow(now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	elapsed := now.Sub(l.last)
	if elapsed > 0 {
		l.tokens += float64(elapsed.Nanoseconds()) * l.refillRate
		if l.tokens > l.maxTokens {
			l.tokens = l.maxTokens
		}
	}
	l.last = now
	if l.tokens >= 1 {
		l.tokens -= 1
		return true, 0
	}
	// seconds until 1 token = (1 - l.tokens) / (refillRate * 1e9)
	if l.refillRate <= 0 {
		return false, 60
	}
	secs := (1 - l.tokens) / (l.refillRate * float64(time.Second/time.Nanosecond))
	if secs < 1 {
		secs = 1
	}
	return false, int(secs + 0.999)
}

// newV2Handler constructs the v2 handler with the supplied base server
// and root operator key. rootKeyPath is REQUIRED in v2 mode; an empty
// path will load-or-generate a key under <source-root>/.r1/skills/
// trust-root.priv.
func newV2Handler(base *skillPackRegistryServer, rootKeyPath string, rateLimit int) (*v2Handler, error) {
	if rootKeyPath == "" {
		rootKeyPath = filepath.Join(base.sourceRoot, ".r1", "skills", "trust-root.priv")
	}
	priv, pub, err := skill.LoadOrGenerateRootKey(rootKeyPath)
	if err != nil {
		return nil, fmt.Errorf("v2 handler: load root key: %w", err)
	}
	h := &v2Handler{
		base:      base,
		rateLimit: rateLimit,
		rootPriv:  priv,
		rootPub:   pub,
	}
	return h, nil
}

// rateLimit middleware. Returns true if the request should proceed.
// On refusal it writes 429 + Retry-After and returns false.
func (h *v2Handler) rateLimitAllow(w http.ResponseWriter, r *http.Request) bool {
	ip := remoteIP(r)
	now := time.Now()
	val, _ := h.limiters.LoadOrStore(ip, newIPLimiter(h.rateLimit))
	lim := val.(*ipLimiter)
	ok, retryAfter := lim.allow(now)
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeRegistryJSONError(w, http.StatusTooManyRequests,
			fmt.Sprintf("Too Many Requests; retry after %ds", retryAfter))
		return false
	}
	return true
}

// remoteIP extracts the source IP for rate-limit keying. Honors
// X-Forwarded-For (single hop) when present; otherwise uses
// r.RemoteAddr's host portion.
func remoteIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		// Take the first hop.
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return xff
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// signResponseHeader computes the X-R1-Registry-Sig over
// SHA256(method + " " + path + "\n" + SHA256(body)) and base64-encodes it.
func (h *v2Handler) signResponseHeader(method, path string, body []byte) string {
	bodySum := sha256.Sum256(body)
	envelope := append([]byte(method+" "+path+"\n"), bodySum[:]...)
	sum := sha256.Sum256(envelope)
	sig := ed25519.Sign(h.rootPriv, sum[:])
	return base64.StdEncoding.EncodeToString(sig)
}

// writeSignedJSON writes a status + JSON body and stamps the
// X-R1-Registry-Sig header before flushing. Used by every v2 handler
// that returns JSON.
func (h *v2Handler) writeSignedJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		writeRegistryJSONError(w, http.StatusInternalServerError, fmt.Sprintf("marshal: %v", err))
		return
	}
	sig := h.signResponseHeader(r.Method, r.URL.Path, body)
	w.Header().Set("X-R1-Registry-Sig", sig)
	w.Header().Set("X-R1-Registry-Pub", base64.StdEncoding.EncodeToString(h.rootPub))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// register attaches v2 routes to the supplied mux.
func (h *v2Handler) register(mux *http.ServeMux) {
	mux.HandleFunc("/v2/packs", h.handleList)
	mux.HandleFunc("/v2/packs/search", h.handleSearch)
	mux.HandleFunc("/v2/trust-root", h.handleTrustRoot)
	mux.HandleFunc("/v2/packs/", h.handlePackPath)
}

// handleList serves GET /v2/packs with optional ?compat= filter.
func (h *v2Handler) handleList(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v2/packs" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	if !h.rateLimitAllow(w, r) {
		return
	}
	if !h.base.authorize(w, r) {
		return
	}
	packPaths, err := registryPackPaths(h.base.sourceRoot)
	if err != nil {
		writeRegistryJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	compatFilter := strings.TrimSpace(r.URL.Query().Get("compat"))
	names := make([]string, 0, len(packPaths))
	for n := range packPaths {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]PackSummaryV2, 0, len(names))
	for _, name := range names {
		detail, err := buildRegistryPackDetailV2(packPaths[name])
		if err != nil {
			writeRegistryJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if compatFilter != "" && !containsStringV2(detail.Compat, compatFilter) {
			continue
		}
		out = append(out, detail.PackSummaryV2)
	}
	h.writeSignedJSON(w, r, http.StatusOK, map[string]any{
		"source_root": h.base.sourceRoot,
		"pack_count":  len(out),
		"packs":       out,
	})
}

// handlePackPath dispatches /v2/packs/{id}[/blob.tar.gz | /sig].
func (h *v2Handler) handlePackPath(w http.ResponseWriter, r *http.Request) {
	if !h.rateLimitAllow(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	if !h.base.authorize(w, r) {
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/v2/packs/")
	if trimmed == "" || trimmed == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(trimmed, "/")
	packName := strings.TrimSpace(parts[0])
	if packName == "" {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(parts) == 1:
		h.handlePackDetail(w, r, packName)
	case len(parts) == 2 && parts[1] == "blob.tar.gz":
		h.handlePackArchive(w, r, packName)
	case len(parts) == 2 && parts[1] == "sig":
		h.handlePackSig(w, r, packName)
	default:
		http.NotFound(w, r)
	}
}

func (h *v2Handler) handlePackDetail(w http.ResponseWriter, r *http.Request, packName string) {
	packPath, err := h.base.resolvePackPath(packName)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeRegistryJSONError(w, status, err.Error())
		return
	}
	detail, err := buildRegistryPackDetailV2(packPath)
	if err != nil {
		writeRegistryJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.writeSignedJSON(w, r, http.StatusOK, detail)
}

func (h *v2Handler) handlePackArchive(w http.ResponseWriter, _ *http.Request, packName string) {
	packPath, err := h.base.resolvePackPath(packName)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeRegistryJSONError(w, status, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", packName+".tar.gz"))
	if err := writePackArchive(w, packPath, packName); err != nil {
		writeRegistryJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
}

func (h *v2Handler) handlePackSig(w http.ResponseWriter, r *http.Request, packName string) {
	packPath, err := h.base.resolvePackPath(packName)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeRegistryJSONError(w, status, err.Error())
		return
	}
	sig, err := skillmfr.ReadPackSignature(packPath)
	if err != nil {
		if errors.Is(err, skillmfr.ErrPackUnsigned) {
			writeRegistryJSONError(w, http.StatusNotFound, "pack is unsigned")
			return
		}
		writeRegistryJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.writeSignedJSON(w, r, http.StatusOK, sig)
}

// handleSearch serves GET /v2/packs/search with q + compat + authority + limit.
func (h *v2Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v2/packs/search" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	if !h.rateLimitAllow(w, r) {
		return
	}
	if !h.base.authorize(w, r) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	compatFilter := strings.TrimSpace(r.URL.Query().Get("compat"))
	authorityFilter := strings.TrimSpace(r.URL.Query().Get("authority"))
	limit := 50
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if n, err := strconv.Atoi(rawLimit); err == nil && n > 0 {
			if n > 200 {
				n = 200
			}
			limit = n
		}
	}

	packPaths, err := registryPackPaths(h.base.sourceRoot)
	if err != nil {
		writeRegistryJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	names := make([]string, 0, len(packPaths))
	for n := range packPaths {
		names = append(names, n)
	}
	sort.Strings(names)

	loweredQ := strings.ToLower(q)
	matches := make([]PackSearchEntryV2, 0, len(names))
	for _, name := range names {
		detail, err := buildRegistryPackDetailV2(packPaths[name])
		if err != nil {
			writeRegistryJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if compatFilter != "" && !containsStringV2(detail.Compat, compatFilter) {
			continue
		}
		if authorityFilter != "" && detail.SignatureAuthority != authorityFilter {
			continue
		}
		matchFields := []string{}
		if loweredQ != "" {
			if strings.Contains(strings.ToLower(detail.Name), loweredQ) {
				matchFields = append(matchFields, "name")
			}
			if strings.Contains(strings.ToLower(detail.Description), loweredQ) {
				matchFields = append(matchFields, "description")
			}
			if len(matchFields) == 0 {
				continue
			}
		}
		matches = append(matches, PackSearchEntryV2{
			PackSummaryV2: detail.PackSummaryV2,
			MatchFields:   matchFields,
		})
		if len(matches) >= limit {
			break
		}
	}
	h.writeSignedJSON(w, r, http.StatusOK, map[string]any{
		"query":       q,
		"match_count": len(matches),
		"matches":     matches,
	})
}

// handleTrustRoot serves GET /v2/trust-root. Loads the document from
// disk (cached 5 min) and signs it on first load if it has no
// signature yet (single-publisher mode).
func (h *v2Handler) handleTrustRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v2/trust-root" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	if !h.rateLimitAllow(w, r) {
		return
	}
	if !h.base.authorize(w, r) {
		return
	}
	doc, err := h.loadOrSynthesizeTrustRoot()
	if err != nil {
		writeRegistryJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.writeSignedJSON(w, r, http.StatusOK, doc)
}

func (h *v2Handler) loadOrSynthesizeTrustRoot() (*skill.TrustRootDocument, error) {
	h.trustRootMu.Lock()
	defer h.trustRootMu.Unlock()
	if h.trustRoot != nil && time.Since(h.trustRootAt) < 5*time.Minute {
		return h.trustRoot, nil
	}
	trustRootPath := filepath.Join(h.base.sourceRoot, ".r1", "skills", "trust-root.json")
	doc, err := skill.LoadTrustRoot(trustRootPath)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		doc = &skill.TrustRootDocument{
			Version:  "1",
			IssuedAt: time.Now().UTC().Format(time.RFC3339),
			Keys:     []skill.TrustRootEntry{},
		}
	}
	if doc.Signature == "" {
		if err := skill.SignTrustRoot(doc, h.rootPriv); err != nil {
			return nil, err
		}
	}
	h.trustRoot = doc
	h.trustRootAt = time.Now()
	return doc, nil
}

// buildRegistryPackDetailV2 builds a v2 detail record for the supplied
// pack path. v1-only packs are auto-upgraded via
// skill.SynthesizeFromV1.
func buildRegistryPackDetailV2(packPath string) (*PackDetailV2, error) {
	pack, signature, err := loadSkillPackWithSignature(packPath)
	if err != nil {
		return nil, err
	}
	manifest, err := skill.LoadManifestV2(packPath)
	if err != nil {
		return nil, err
	}
	return &PackDetailV2{
		PackSummaryV2: PackSummaryV2{
			Name:                  pack.Meta.Name,
			Version:               pack.Meta.Version,
			Description:           pack.Meta.Description,
			ManifestSchemaVersion: manifest.SchemaVersion,
			Compat:                append([]string(nil), manifest.Compat...),
			SignatureAuthority:    string(manifest.SignatureAuthority),
			Signed:                signature != nil,
			SignatureKeyID:        signatureKeyID(signature),
			DownloadURLPath:       fmt.Sprintf("/v2/packs/%s/blob.tar.gz", pack.Meta.Name),
			SigURLPath:            fmt.Sprintf("/v2/packs/%s/sig", pack.Meta.Name),
		},
		Dependencies:      append([]string(nil), manifest.Dependencies...),
		RuntimeAssertions: manifest.RuntimeAssertions,
		ConsumerHooks:     manifest.ConsumerHooks,
		MinR1Version:      manifest.MinR1Version,
		SourcePath:        packPath,
		SignatureEnvelope: signature,
	}, nil
}

func containsStringV2(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// resolveV2RateLimit reads the rate limit from env, falling back to
// defaultV2RateLimit. Values <=0 fall back too — operators cannot
// turn off the limiter via env (use a different middleware if you
// need that).
func resolveV2RateLimit() int {
	raw := strings.TrimSpace(r1env.Get(v2RateLimitEnv, ""))
	if raw == "" {
		return defaultV2RateLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultV2RateLimit
	}
	return n
}

// resolveV2RootKeyPath returns the configured root operator key path.
// Empty when neither env nor default exists; callers handle the
// "load-or-generate" choice.
func resolveV2RootKeyPath() string {
	if path := strings.TrimSpace(r1env.Get(v2RootKeyEnv, "")); path != "" {
		return path
	}
	return ""
}

// startLimiterEvictor periodically scans h.limiters and drops entries
// whose last-allow timestamp is older than idleLimiterEvictAfter.
// Bounded goroutine; callers cancel by closing stop.
func (h *v2Handler) startLimiterEvictor(stop <-chan struct{}) {
	go func() {
		tick := time.NewTicker(idleLimiterEvictAfter / 2)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				now := time.Now()
				h.limiters.Range(func(k, v any) bool {
					lim := v.(*ipLimiter)
					lim.mu.Lock()
					stale := now.Sub(lim.last) > idleLimiterEvictAfter
					lim.mu.Unlock()
					if stale {
						h.limiters.Delete(k)
					}
					return true
				})
			}
		}
	}()
}

// warnNoTLS prints a one-line stderr warning when serve runs without
// TLS. Centralized so the test harness can match the exact string.
func warnNoTLS(addr string) {
	fmt.Fprintf(os.Stderr, "skills pack serve: warning: serving HTTP without TLS on %s; production deployments MUST use --cert/--key\n", addr)
}
