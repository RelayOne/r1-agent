// Dashboard API endpoints expose task state, cost tracking, pool utilization,
// and environment status over HTTP for the Stoke web dashboard.
//
// WebSocket streaming at /api/ws provides real-time event delivery as an
// alternative to the existing SSE endpoint at /api/events.
package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/costtrack"
	"github.com/ericmacdougall/stoke/internal/subscriptions"
)

// DashboardAPI provides HTTP handlers for the dashboard.
type DashboardAPI struct {
	bus     *EventBus
	cost    *costtrack.Tracker
	pools   *subscriptions.Manager
	state   *DashboardState
}

// DashboardState holds the current snapshot of task states for dashboard queries.
// Updated by the hub bridge as events flow in.
type DashboardState struct {
	mu    sync.RWMutex
	tasks map[string]*TaskSnapshot
}

// TaskSnapshot is a point-in-time view of a task for the dashboard.
type TaskSnapshot struct {
	ID          string    `json:"id"`
	Description string    `json:"description,omitempty"`
	Phase       string    `json:"phase"`
	Status      string    `json:"status"` // pending, running, completed, failed, retrying
	Attempt     int       `json:"attempt"`
	CostUSD     float64   `json:"cost_usd"`
	DurationMs  int64     `json:"duration_ms"`
	Worker      string    `json:"worker,omitempty"`
	LastTool    string    `json:"last_tool,omitempty"`
	Error       string    `json:"error,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NewDashboardState creates an empty dashboard state tracker.
func NewDashboardState() *DashboardState {
	return &DashboardState{tasks: make(map[string]*TaskSnapshot)}
}

// Update upserts a task snapshot. Called from the hub bridge.
func (ds *DashboardState) Update(snap TaskSnapshot) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	snap.UpdatedAt = time.Now()
	ds.tasks[snap.ID] = &snap
}

// All returns a copy of all task snapshots.
func (ds *DashboardState) All() []TaskSnapshot {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	out := make([]TaskSnapshot, 0, len(ds.tasks))
	for _, t := range ds.tasks {
		out = append(out, *t)
	}
	return out
}

// Get returns a single task snapshot, or nil if not found.
func (ds *DashboardState) Get(id string) *TaskSnapshot {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	t, ok := ds.tasks[id]
	if !ok {
		return nil
	}
	cp := *t
	return &cp
}

// RegisterDashboardAPI adds dashboard endpoints to the server.
// Pass nil for any optional dependency to disable that endpoint's data.
func RegisterDashboardAPI(s *Server, cost *costtrack.Tracker, pools *subscriptions.Manager, state *DashboardState) {
	api := &DashboardAPI{
		bus:   s.Bus,
		cost:  cost,
		pools: pools,
		state: state,
	}

	s.mux.HandleFunc("/api/dashboard/tasks", s.authWrap(api.handleTasks))
	s.mux.HandleFunc("/api/dashboard/tasks/get", s.authWrap(api.handleTaskGet))
	s.mux.HandleFunc("/api/dashboard/cost", s.authWrap(api.handleCost))
	s.mux.HandleFunc("/api/dashboard/pools", s.authWrap(api.handlePools))
	s.mux.HandleFunc("/api/dashboard/summary", s.authWrap(api.handleSummary))
	s.mux.HandleFunc("/api/ws", s.authWrap(api.handleWebSocket))
}

// --- Tasks ---

func (a *DashboardAPI) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.state == nil {
		writeJSON(w, map[string]interface{}{"tasks": []TaskSnapshot{}, "count": 0})
		return
	}

	tasks := a.state.All()

	// Optional status filter.
	status := r.URL.Query().Get("status")
	if status != "" {
		var filtered []TaskSnapshot
		for _, t := range tasks {
			if t.Status == status {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}

	writeJSON(w, map[string]interface{}{"tasks": tasks, "count": len(tasks)})
}

func (a *DashboardAPI) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id parameter")
		return
	}
	if a.state == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	snap := a.state.Get(id)
	if snap == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, snap)
}

// --- Cost ---

func (a *DashboardAPI) handleCost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.cost == nil {
		writeJSON(w, map[string]interface{}{
			"total_usd": 0, "env_usd": 0, "by_model": map[string]float64{},
			"by_task": map[string]float64{}, "requests": 0,
		})
		return
	}

	input, output, cacheRead, cacheWrite := a.cost.TokenTotals()
	writeJSON(w, map[string]interface{}{
		"total_usd":    a.cost.Total(),
		"env_usd":      a.cost.EnvCost(),
		"budget_remain": a.cost.BudgetRemaining(),
		"over_budget":  a.cost.OverBudget(),
		"by_model":     a.cost.ByModel(),
		"by_task":      a.cost.ByTask(),
		"requests":     a.cost.RequestCount(),
		"tokens": map[string]int{
			"input":       input,
			"output":      output,
			"cache_read":  cacheRead,
			"cache_write": cacheWrite,
		},
	})
}

// --- Pools ---

func (a *DashboardAPI) handlePools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.pools == nil {
		writeJSON(w, map[string]interface{}{"pools": []interface{}{}, "count": 0})
		return
	}

	snapshot := a.pools.Snapshot()
	type poolView struct {
		ID          string    `json:"id"`
		Provider    string    `json:"provider"`
		Status      string    `json:"status"`
		CurrentTask string    `json:"current_task,omitempty"`
		Utilization float64   `json:"utilization"`
		ResetsAt    time.Time `json:"resets_at,omitempty"`
	}
	views := make([]poolView, len(snapshot))
	for i, p := range snapshot {
		views[i] = poolView{
			ID:          p.ID,
			Provider:    string(p.Provider),
			Status:      p.Status.String(),
			CurrentTask: p.CurrentTask,
			Utilization: p.Utilization,
			ResetsAt:    p.ResetsAt,
		}
	}
	writeJSON(w, map[string]interface{}{"pools": views, "count": len(views)})
}

// --- Summary ---

func (a *DashboardAPI) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	summary := map[string]interface{}{
		"timestamp": time.Now(),
	}

	if a.state != nil {
		tasks := a.state.All()
		counts := map[string]int{}
		for _, t := range tasks {
			counts[t.Status]++
		}
		summary["tasks"] = map[string]interface{}{
			"total":  len(tasks),
			"counts": counts,
		}
	}

	if a.cost != nil {
		summary["cost"] = map[string]interface{}{
			"total_usd":   a.cost.Total(),
			"env_usd":     a.cost.EnvCost(),
			"over_budget": a.cost.OverBudget(),
			"requests":    a.cost.RequestCount(),
		}
	}

	if a.pools != nil {
		snap := a.pools.Snapshot()
		active := 0
		for _, p := range snap {
			if p.CurrentTask != "" {
				active++
			}
		}
		summary["pools"] = map[string]interface{}{
			"total":  len(snap),
			"active": active,
		}
	}

	writeJSON(w, summary)
}

// --- WebSocket ---
// Minimal RFC 6455 WebSocket implementation using net/http.Hijacker.
// Sends JSON events to clients; does not expect incoming messages.

func (a *DashboardAPI) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "websocket" {
		http.Error(w, "expected websocket upgrade", http.StatusBadRequest)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket unsupported", http.StatusInternalServerError)
		return
	}

	// Compute accept key per RFC 6455.
	key := r.Header.Get("Sec-WebSocket-Key")
	acceptKey := computeAcceptKey(key)

	conn, bufrw, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	// Send upgrade response.
	bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	bufrw.WriteString("Upgrade: websocket\r\n")
	bufrw.WriteString("Connection: Upgrade\r\n")
	bufrw.WriteString("Sec-WebSocket-Accept: " + acceptKey + "\r\n")
	bufrw.WriteString("\r\n")
	bufrw.Flush()

	// Subscribe to event bus.
	ch := a.bus.Subscribe()
	defer a.bus.Unsubscribe(ch)

	// Drain incoming frames (we don't process them, but must read to detect close).
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainFrames(bufrw.Reader)
	}()

	for {
		select {
		case <-done:
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := writeWSTextFrame(bufrw.Writer, []byte(msg)); err != nil {
				return
			}
			bufrw.Flush()
		}
	}
}

// computeAcceptKey generates the Sec-WebSocket-Accept value per RFC 6455.
func computeAcceptKey(clientKey string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.New()
	io.WriteString(h, clientKey+magic)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// writeWSTextFrame writes a WebSocket text frame (opcode 0x1).
func writeWSTextFrame(w *bufio.Writer, payload []byte) error {
	// FIN + text opcode
	w.WriteByte(0x81)
	length := len(payload)
	switch {
	case length <= 125:
		w.WriteByte(byte(length))
	case length <= 65535:
		w.WriteByte(126)
		var buf [2]byte
		binary.BigEndian.PutUint16(buf[:], uint16(length))
		w.Write(buf[:])
	default:
		w.WriteByte(127)
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(length))
		w.Write(buf[:])
	}
	_, err := w.Write(payload)
	return err
}

// drainFrames reads and discards WebSocket frames until the connection closes.
func drainFrames(r *bufio.Reader) {
	buf := make([]byte, 512)
	for {
		if _, err := r.Read(buf); err != nil {
			return
		}
	}
}

// wsConn wraps a hijacked connection for testing.
type wsConn struct {
	w    *bufio.Writer
	r    *bufio.Reader
	done chan struct{}
}

// Ping sends a WebSocket ping frame.
func (c *wsConn) Ping() error {
	c.w.WriteByte(0x89) // FIN + ping opcode
	c.w.WriteByte(0)    // zero-length payload
	return c.w.Flush()
}

// ReadMessage reads the next WebSocket text frame payload.
func (c *wsConn) ReadMessage() (string, error) {
	// Read frame header.
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.r, header); err != nil {
		return "", err
	}
	length := int(header[1] & 0x7F)
	switch length {
	case 126:
		var buf [2]byte
		if _, err := io.ReadFull(c.r, buf[:]); err != nil {
			return "", err
		}
		length = int(binary.BigEndian.Uint16(buf[:]))
	case 127:
		var buf [8]byte
		if _, err := io.ReadFull(c.r, buf[:]); err != nil {
			return "", err
		}
		length = int(binary.BigEndian.Uint64(buf[:]))
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return "", err
	}
	return string(payload), nil
}

// Close sends a WebSocket close frame.
func (c *wsConn) Close() error {
	c.w.WriteByte(0x88) // FIN + close opcode
	c.w.WriteByte(0)    // zero-length payload
	return c.w.Flush()
}

// formatDuration formats a duration in human-readable form for the dashboard.
func formatDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return fmt.Sprintf("%dms", ms)
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}
