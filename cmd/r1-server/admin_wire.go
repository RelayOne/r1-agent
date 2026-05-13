// Package main — A5 admin panel wiring.
//
// admin_wire.go mounts the read-only /admin/* and /api/admin/* routes
// from `internal/server.AdminRoutes` onto the existing r1-server
// ServeMux. Auth gating reuses A4's RequireAdmin middleware; dev-bypass
// is honored via R1_ADMIN_DEV_BYPASS so a local operator can hit the
// pages without a real SSO token.
//
// The wire-in is intentionally minimal: no DB-level integration with
// sessions or billing yet, just enough for the spec's "five read-only
// routes exist and gate properly" acceptance check. Full data wiring
// follows in a Phase-2 spec.

package main

import (
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/RelayOne/r1/internal/server"
	"github.com/RelayOne/r1/internal/tenants"
)

//go:embed templates/admin/*
var adminTemplatesFS embed.FS

// loadAdminTemplates parses every file under templates/admin/ into one
// associated template set. admin-base.html defines the layout block;
// each *.tmpl file extends it via {{ template "admin-base" . }}.
func loadAdminTemplates() (*template.Template, error) {
	t := template.New("admin")

	entries, err := adminTemplatesFS.ReadDir("templates/admin")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := adminTemplatesFS.ReadFile(filepath.Join("templates/admin", e.Name()))
		if err != nil {
			return nil, err
		}
		// Name each file template after its filename (matches the
		// existing convention where templates are referenced by base
		// name in {{ template "X" }} directives).
		if _, err := t.New(e.Name()).Parse(string(body)); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// mountAdmin attaches AdminRoutes(mux, deps, gate) to the supplied mux.
// Returns silently when admin-template load fails or when the operator
// hasn't enabled the admin surface — a fresh dev start without any
// admin configuration must not refuse to come up.
func mountAdmin(mux *http.ServeMux, _ *DB, logger *slog.Logger) {
	tpl, err := loadAdminTemplates()
	if err != nil {
		logger.Warn("admin panel disabled: template load failed", "err", err)
		return
	}

	deps := server.AdminDeps{
		Tenants:   loadTenantStore(logger),
		Sessions:  nil, // r1-server is the observation daemon; live SessionHub not wired here
		Cost:      nil, // costtrack wired in a follow-up; nil renders an empty billing page
		AntiTrunc: server.NewAntiTruncBuffer(server.DefaultAntiTruncBufferSize),
		Audit:     emptyAuditReader{}, // ledger adapter is a follow-up
		Emitter:   adminViewEmitter(logger),
		Templates: tpl,
	}

	devBypass := os.Getenv("R1_ADMIN_DEV_BYPASS") == "1"
	gate := server.RequireAdmin(nil, server.AdminMiddlewareConfig{
		SSOStartPath: "/auth/sso/start",
		RequiredRole: "admin",
		DevBypass:    devBypass,
	})

	server.AdminRoutes(mux, deps, gate)
	logger.Info("admin routes mounted", "dev_bypass", devBypass)
}

// loadTenantStore reads ~/.r1/tenants.json when present; empty store
// otherwise. The admin panel is read-only, so an empty store renders
// an empty list rather than a 500.
func loadTenantStore(logger *slog.Logger) tenants.Store {
	home, err := os.UserHomeDir()
	if err != nil {
		return tenants.NewStaticStoreFromMemory(nil)
	}
	path := filepath.Join(home, ".r1", "tenants.json")
	store, err := tenants.NewStaticStore(path)
	if err != nil {
		logger.Info("admin: no tenants.json present; rendering empty list", "path", path)
		return tenants.NewStaticStoreFromMemory(nil)
	}
	return store
}

// emptyAuditReader returns zero ledger rows. r1-server is the
// observation daemon and the ledger adapter needs the full ledger
// Store handle which the daemon doesn't construct at this layer —
// a follow-up commit wires `internal/ledger.Store.RangeForAdmin`
// through to here. Until then the audit page renders an empty table.
type emptyAuditReader struct{}

func (emptyAuditReader) Range(server.AuditFilter) ([]server.AuditRow, int, error) {
	return nil, 0, nil
}

// adminViewEmitter logs every admin page view at info level. The
// production wiring will additionally write an AdminViewed ledger
// node; tests use a channel-backed fake. Logging here is enough for
// the v1 audit-trail floor.
func adminViewEmitter(logger *slog.Logger) server.AdminViewedEmitter {
	return func(_ context.Context, v server.AdminView) {
		logger.Info("admin.viewed",
			"path", v.Path,
			"user", v.User,
			"tenant", v.Tenant,
			"remote", v.RemoteAddr,
			"method", v.Method,
			"status", v.Status,
		)
	}
}
