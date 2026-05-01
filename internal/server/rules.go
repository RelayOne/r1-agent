package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/RelayOne/r1/internal/rules"
)

type RulesAPI struct {
	registry *rules.Registry
}

type addRuleRequest struct {
	Text                string `json:"text"`
	Scope               string `json:"scope"`
	ToolFilter          string `json:"tool_filter"`
	EnforcementStrategy string `json:"enforcement_strategy"`
}

func RegisterRulesAPI(s *Server, repoRoot string) {
	api := &RulesAPI{
		registry: rules.NewRepoRegistry(repoRoot, nil),
	}
	s.mux.HandleFunc("/rules", s.authWrap(api.handleRules))
	s.mux.HandleFunc("/rules/", s.authWrap(api.handleRuleByID))
}

func (a *RulesAPI) handleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if wantsJSON(r) {
			a.writeRulesJSON(w)
			return
		}
		a.serveRulesPage(w, r)
	case http.MethodPost:
		var req addRuleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		rule, err := a.registry.AddWithOptions(r.Context(), rules.AddRequest{
			Text:             req.Text,
			Scope:            req.Scope,
			ToolFilter:       req.ToolFilter,
			StrategyOverride: req.EnforcementStrategy,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, rule)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *RulesAPI) handleRuleByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/rules/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case r.Method == http.MethodDelete && action == "":
		if err := a.registry.Delete(id); err != nil {
			writeRuleError(w, err)
			return
		}
		writeJSON(w, map[string]string{"status": "deleted", "id": id})
	case r.Method == http.MethodPost && action == "pause":
		if err := a.registry.Pause(id); err != nil {
			writeRuleError(w, err)
			return
		}
		writeJSON(w, map[string]string{"status": "paused", "id": id})
	case r.Method == http.MethodPost && action == "resume":
		if err := a.registry.Resume(id); err != nil {
			writeRuleError(w, err)
			return
		}
		writeJSON(w, map[string]string{"status": "active", "id": id})
	case r.Method == http.MethodGet && action == "":
		rule, err := a.registry.Get(id)
		if err != nil {
			writeRuleError(w, err)
			return
		}
		writeJSON(w, rule)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *RulesAPI) writeRulesJSON(w http.ResponseWriter) {
	list, err := a.registry.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"rules": list,
		"count": len(list),
	})
}

func (a *RulesAPI) serveRulesPage(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/rules.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func wantsJSON(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "json") {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func writeRuleError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, http.ErrMissingFile) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}
