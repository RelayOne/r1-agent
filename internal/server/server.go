package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// EventBus distributes events to SSE clients.
type EventBus struct {
	mu      sync.Mutex
	clients map[chan string]bool
}

func NewEventBus() *EventBus {
	return &EventBus{clients: make(map[chan string]bool)}
}

func (b *EventBus) Subscribe() chan string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan string, 64)
	b.clients[ch] = true
	return ch
}

func (b *EventBus) Unsubscribe(ch chan string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.clients, ch)
	close(ch)
}

func (b *EventBus) Publish(event string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- event:
		default: // drop if buffer full
		}
	}
}

// Server provides HTTP API for monitoring Stoke builds.
type Server struct {
	Port     int
	Token    string           // bearer token for auth (empty = no auth)
	Bus      *EventBus
	StatusFn func() interface{} // returns current build status
	mux      *http.ServeMux
}

func New(port int, token string, bus *EventBus) *Server {
	s := &Server{Port: port, Token: token, Bus: bus, mux: http.NewServeMux()}
	s.mux.HandleFunc("/api/status", s.authWrap(s.handleStatus))
	s.mux.HandleFunc("/api/events", s.authWrap(s.handleEvents))
	s.mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	return s
}

func (s *Server) authWrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+s.Token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		h(w, r)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.StatusFn != nil {
		json.NewEncoder(w).Encode(s.StatusFn())
	} else {
		json.NewEncoder(w).Encode(map[string]string{"status": "running"})
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.Bus.Subscribe()
	defer s.Bus.Unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

// Handler returns the HTTP handler for use with httptest or custom servers.
func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) ListenAndServe() error {
	// Use a configured *http.Server (not the bare http.ListenAndServe
	// helper) so we can set ReadHeaderTimeout — the bare helper gives
	// attackers free Slowloris and slow-body budget. 10s ReadHeader +
	// 60s overall are above any legitimate client of a local dashboard
	// while capping the attack window.
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.Port),
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}
