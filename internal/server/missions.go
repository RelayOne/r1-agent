// Mission API endpoints expose the mission lifecycle over HTTP.
//
// These endpoints allow external consumers (CLI, TUI, MCP clients, CI/CD)
// to create missions, check status, run convergence, record handoffs,
// and manage the full mission lifecycle.
//
// All endpoints are JSON-based and use the same auth wrapper as the
// existing /api/status and /api/events endpoints.
package server

import (
	"encoding/json"
	"net/http"

	"github.com/ericmacdougall/stoke/internal/convergence"
	"github.com/ericmacdougall/stoke/internal/handoff"
	"github.com/ericmacdougall/stoke/internal/mission"
	"github.com/ericmacdougall/stoke/internal/orchestrate"
)

// MissionAPI provides HTTP handlers for mission management.
// Attach it to a Server via RegisterMissionAPI.
type MissionAPI struct {
	orch *orchestrate.Orchestrator
	bus  *EventBus
}

// RegisterMissionAPI adds mission endpoints to the server.
// Pass nil for orch to skip registration (for when missions are disabled).
func RegisterMissionAPI(s *Server, orch *orchestrate.Orchestrator) {
	if orch == nil {
		return
	}
	api := &MissionAPI{orch: orch, bus: s.Bus}

	s.mux.HandleFunc("/api/missions", s.authWrap(api.handleMissions))
	s.mux.HandleFunc("/api/missions/create", s.authWrap(api.handleCreateMission))
	s.mux.HandleFunc("/api/missions/get", s.authWrap(api.handleGetMission))
	s.mux.HandleFunc("/api/missions/advance", s.authWrap(api.handleAdvanceMission))
	s.mux.HandleFunc("/api/missions/convergence", s.authWrap(api.handleConvergence))
	s.mux.HandleFunc("/api/missions/convergence/run", s.authWrap(api.handleRunConvergence))
	s.mux.HandleFunc("/api/missions/findings", s.authWrap(api.handleFindings))
	s.mux.HandleFunc("/api/missions/context", s.authWrap(api.handleBuildContext))
	s.mux.HandleFunc("/api/missions/handoff", s.authWrap(api.handleRecordHandoff))
	s.mux.HandleFunc("/api/missions/consensus", s.authWrap(api.handleRecordConsensus))
}

// --- List Missions ---

func (a *MissionAPI) handleMissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	phase := mission.Phase(r.URL.Query().Get("phase"))
	missions, err := a.orch.ListMissions(phase)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]interface{}{
		"missions": missions,
		"count":    len(missions),
	})
}

// --- Create Mission ---

type createMissionRequest struct {
	Title    string   `json:"title"`
	Intent   string   `json:"intent"`
	Criteria []string `json:"criteria"`
}

func (a *MissionAPI) handleCreateMission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req createMissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	m, err := a.orch.CreateMission(req.Title, req.Intent, req.Criteria)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	a.bus.Publish(mustJSON(map[string]string{
		"event": "mission_created", "mission_id": m.ID, "title": m.Title,
	}))

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, m)
}

// --- Get Mission ---

func (a *MissionAPI) handleGetMission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id parameter")
		return
	}

	m, err := a.orch.GetMission(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if m == nil {
		writeError(w, http.StatusNotFound, "mission not found")
		return
	}

	writeJSON(w, m)
}

// --- Advance Mission ---

type advanceRequest struct {
	MissionID string `json:"mission_id"`
	Phase     string `json:"phase"`
	Reason    string `json:"reason"`
	Agent     string `json:"agent"`
}

func (a *MissionAPI) handleAdvanceMission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req advanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := a.orch.AdvanceMission(req.MissionID, mission.Phase(req.Phase), req.Reason, req.Agent); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	a.bus.Publish(mustJSON(map[string]string{
		"event": "mission_advanced", "mission_id": req.MissionID, "phase": req.Phase,
	}))

	writeJSON(w, map[string]string{"status": "ok", "phase": req.Phase})
}

// --- Check Convergence Status ---

func (a *MissionAPI) handleConvergence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id parameter")
		return
	}

	status, err := a.orch.CheckConvergence(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, status)
}

// --- Run Convergence Validation ---

type runConvergenceRequest struct {
	MissionID string                  `json:"mission_id"`
	Files     []convergence.FileInput `json:"files"`
}

func (a *MissionAPI) handleRunConvergence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req runConvergenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	report, err := a.orch.RunConvergence(req.MissionID, req.Files)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	a.bus.Publish(mustJSON(map[string]interface{}{
		"event": "convergence_run", "mission_id": req.MissionID,
		"converged": report.IsConverged, "score": report.Score,
		"findings": len(report.Findings),
	}))

	writeJSON(w, report)
}

// --- Get Findings (Gaps) ---

func (a *MissionAPI) handleFindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id parameter")
		return
	}

	includeResolved := r.URL.Query().Get("all") == "true"
	severity := r.URL.Query().Get("severity")
	category := r.URL.Query().Get("category")

	var gaps []mission.Gap
	var err error
	if includeResolved {
		gaps, err = a.orch.AllGaps(id)
	} else {
		gaps, err = a.orch.OpenGaps(id)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Apply filters
	var filtered []mission.Gap
	for _, g := range gaps {
		if severity != "" && g.Severity != severity {
			continue
		}
		if category != "" && g.Category != category {
			continue
		}
		filtered = append(filtered, g)
	}

	writeJSON(w, map[string]interface{}{
		"mission_id": id,
		"findings":   filtered,
		"count":      len(filtered),
		"total":      len(gaps),
	})
}

// --- Build Agent Context ---

type buildContextRequest struct {
	MissionID string                `json:"mission_id"`
	Config    *mission.ContextConfig `json:"config,omitempty"`
}

func (a *MissionAPI) handleBuildContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req buildContextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	config := mission.DefaultContextConfig()
	if req.Config != nil {
		config = *req.Config
	}

	ctx, err := a.orch.BuildAgentContext(req.MissionID, config)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, map[string]string{"context": ctx})
}

// --- Record Handoff ---

func (a *MissionAPI) handleRecordHandoff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var record handoff.Record
	if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := a.orch.RecordHandoff(record); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	a.bus.Publish(mustJSON(map[string]string{
		"event": "handoff", "mission_id": record.MissionID,
		"from": record.FromAgent, "to": record.ToAgent,
	}))

	writeJSON(w, map[string]string{"status": "ok"})
}

// --- Record Consensus ---

type consensusRequest struct {
	MissionID string   `json:"mission_id"`
	Model     string   `json:"model"`
	Verdict   string   `json:"verdict"`
	Reasoning string   `json:"reasoning"`
	GapsFound []string `json:"gaps_found"`
}

func (a *MissionAPI) handleRecordConsensus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req consensusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := a.orch.RecordConsensus(req.MissionID, req.Model, req.Verdict, req.Reasoning, req.GapsFound); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	a.bus.Publish(mustJSON(map[string]string{
		"event": "consensus", "mission_id": req.MissionID,
		"model": req.Model, "verdict": req.Verdict,
	}))

	has, _ := a.orch.HasConsensus(req.MissionID)
	writeJSON(w, map[string]interface{}{
		"status":        "ok",
		"has_consensus": has,
	})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
