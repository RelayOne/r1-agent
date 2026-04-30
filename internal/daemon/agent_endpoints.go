package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type agentSessionCreateRequest struct {
	AgentID      string   `json:"agent_id"`
	Capabilities []string `json:"capabilities"`
}

type agentSessionCreateResponse struct {
	SessionID string `json:"session_id"`
	Token     string `json:"token"`
}

type agentChatRequest struct {
	SessionID   string `json:"session_id"`
	Message     string `json:"message"`
	MessageType string `json:"message_type"`
}

type agentChatResponse struct {
	Reply           string   `json:"reply"`
	CurrentState    string   `json:"current_state"`
	TaskIDsAffected []string `json:"task_ids_affected,omitempty"`
}

type agentFollowUpRequest struct {
	SessionID    string `json:"session_id"`
	ParentTaskID string `json:"parent_task_id"`
	NewContext   string `json:"new_context"`
}

type agentFollowUpResponse struct {
	NewTaskID      string `json:"new_task_id"`
	WillReplayFrom string `json:"will_replay_from"`
}

func (d *Daemon) registerAgentRoutes() {
	d.mux.HandleFunc("/agent/session", d.auth(d.handleAgentSessionCreate))
	d.mux.HandleFunc("/agent/chat", d.handleAgentChat)
	d.mux.HandleFunc("/agent/events", d.handleAgentEvents)
	d.mux.HandleFunc("/agent/follow-up", d.handleAgentFollowUp)
}

func (d *Daemon) handleAgentSessionCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agentSessionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	sess, err := d.agentSessions.Create(req.AgentID, req.Capabilities)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, agentSessionCreateResponse{SessionID: sess.ID, Token: sess.Token})
}

func (d *Daemon) handleAgentChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agentChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}
	if _, err := d.agentSessions.Authorize(req.SessionID, r.Header.Get("Authorization"), d.cfg.Token); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	reply, state, taskIDs, err := d.agentSessions.Chat(r.Context(), req.SessionID, req.Message, req.MessageType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, agentChatResponse{
		Reply:           reply,
		CurrentState:    state,
		TaskIDsAffected: taskIDs,
	})
}

func (d *Daemon) handleAgentEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}
	if _, err := d.agentSessions.Authorize(sessionID, r.Header.Get("Authorization"), d.cfg.Token); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	since, err := parseAgentSince(r.URL.Query().Get("since"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	backlog, err := d.agentSessions.EventsSince(sessionID, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	ch, cancel, err := d.agentSessions.Subscribe(sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	for _, ev := range backlog {
		if !writeAgentEvent(w, ev) {
			return
		}
		flusher.Flush()
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !writeAgentEvent(w, ev) {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

func (d *Daemon) handleAgentFollowUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agentFollowUpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.ParentTaskID) == "" {
		http.Error(w, "session_id and parent_task_id required", http.StatusBadRequest)
		return
	}
	if _, err := d.agentSessions.Authorize(req.SessionID, r.Header.Get("Authorization"), d.cfg.Token); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	newTaskID, replayFrom, err := d.agentSessions.FollowUp(req.SessionID, req.ParentTaskID, req.NewContext)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, agentFollowUpResponse{NewTaskID: newTaskID, WillReplayFrom: replayFrom})
}

func parseAgentSince(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(unix, 0).UTC(), nil
	}
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid since %q", raw)
}

func writeAgentEvent(w http.ResponseWriter, ev AgentEvent) bool {
	body, err := json.Marshal(ev)
	if err != nil {
		return false
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", body)
	return err == nil
}
