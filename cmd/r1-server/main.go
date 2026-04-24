// Package main — r1-server
//
// Visual execution-trace daemon for Stoke. Discovers running Stoke
// instances via their <repo>/.stoke/r1.session.json signature files,
// ingests their event streams + ledger DAG, and exposes the data
// over HTTP (port 3948 by default). One r1-server runs per machine;
// multiple Stoke instances register with it concurrently.
//
// RS-2 items covered by this file:
//
//	1. new binary cmd/r1-server/main.go
//	4. HTTP server on port 3948 (override via R1_SERVER_PORT)
//	5. API endpoints — register, health, sessions, session detail,
//	   events, ledger, checkpoints
//	6. Graceful shutdown on SIGINT/SIGTERM
//	7. Single-instance guard (listener bound before Serve)
//	8. Structured logging via log/slog to <data_dir>/r1-server.log
//	   with 10MB rotation
//
// RS-2 items 2 (datadir) and 3 (DB schema) live in datadir.go + db.go.
// RS-3 (scanner + event tailer) will be added in a follow-up commit.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/ericmacdougall/stoke/internal/retention"
	"github.com/ericmacdougall/stoke/internal/session"
)

// Version is the build-identifier reported by /api/health and
// `r1-server --version`. Set at link time via -ldflags if needed.
var Version = "0.1.0-dev"

const (
	defaultPort = 3948
	logMaxBytes = 10 * 1024 * 1024 // 10MB per spec RS-2 item 8
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "r1-server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println(Version)
		return nil
	}

	dataDir, err := ensureDataDir()
	if err != nil {
		return err
	}

	// Resolve port before opening the log file so a bad
	// R1_SERVER_PORT doesn't leave a dangling file handle.
	port, err := resolvePort()
	if err != nil {
		return fmt.Errorf("resolve port: %w", err)
	}

	logger, logFile, err := openLogger(dataDir)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer logFile.Close()
	slog.SetDefault(logger)

	// Single-instance guard (RS-2 item 7). Bind the listener BEFORE
	// opening the DB so a second instance exits without touching the
	// database file. A clear stderr message also goes to the operator
	// so they know why the process died.
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"r1-server: cannot bind :%d — another r1-server may be running (%v)\n",
			port, err)
		logger.Error("bind listener", "port", port, "err", err)
		return fmt.Errorf("bind :%d: %w", port, err)
	}

	db, err := OpenDB(dataDir)
	if err != nil {
		ln.Close()
		logger.Error("open db", "err", err)
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	mux := buildMux(db, logger)
	// RS-4: embedded SPA + static assets mounted alongside the API.
	// Spec 27 adds /memories (DB-backed) and /settings (file-backed)
	// read-only views, so mountUI needs the DB handle.
	mountUI(mux, db)

	// work-stoke TASK 15: self-check that the 3D UI vendor bundle is
	// present. Logs a single WARNING on a fresh checkout where the
	// operator has not yet populated cmd/r1-server/ui/vendor/; never
	// fatal — graph.html falls back to its CDN <script> tags.
	checkVendoredLibs(uiFS, logger)

	srv := &http.Server{
		Handler:           loggingMiddleware(logger, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// RS-3: background scanner + per-session tailer. Shares the
	// shutdown context below so SIGINT drains scan/tail goroutines
	// in lockstep with the HTTP server.
	scannerCtx, cancelScanner := context.WithCancel(context.Background())
	defer cancelScanner()
	scanner := NewScanner(db, ScannerConfig{Logger: logger})
	scanner.Start(scannerCtx)

	// work-stoke T10 / specs/retention-policies.md §6-7: hourly
	// retention sweep. r1-server is a read-only observation daemon
	// so it does not own a membus handle — retention.EnforceSweep
	// handles a nil bus by skipping the memory-bus TTL delete and
	// sweeping only stream + checkpoint files on disk, which is the
	// correct best-effort behavior for a per-machine r1-server. The
	// Task 6 crypto-shred integration lands in a follow-up commit.
	sweepWG := startRetentionSweep(scannerCtx, logger, time.Hour, retention.Defaults(), nil)

	// RS-2 item 6: graceful shutdown. SIGINT/SIGTERM → stop accepting,
	// drain in-flight, close DB via defers. 10s hard deadline keeps a
	// stuck connection from wedging shutdown.
	shutdownCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("r1-server listening",
			"port", port, "data_dir", dataDir, "version", Version)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("shutdown signal", "signal", sig.String())
	case err := <-errCh:
		if err != nil {
			logger.Error("serve", "err", err)
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-shutdownCtx.Done():
	}

	shutdownDeadline, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownDeadline); err != nil {
		logger.Warn("shutdown", "err", err)
	}
	// Drain scanner goroutines after the HTTP server stops accepting.
	cancelScanner()
	scanner.Wait()
	sweepWG.Wait()
	logger.Info("shutdown complete")
	return nil
}

// resolvePort reads R1_SERVER_PORT and falls back to the default.
func resolvePort() (int, error) {
	raw := os.Getenv("R1_SERVER_PORT")
	if raw == "" {
		return defaultPort, nil
	}
	p, err := strconv.Atoi(raw)
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("R1_SERVER_PORT must be a 1-65535 integer, got %q", raw)
	}
	return p, nil
}

// openLogger builds a slog handler writing to r1-server.log under
// dataDir with naive size-based rotation. Returns the handler, the
// underlying writer (for defer Close), and any error.
func openLogger(dataDir string) (*slog.Logger, io.Closer, error) {
	path := filepath.Join(dataDir, "r1-server.log")
	rw, err := newRotatingWriter(path, logMaxBytes)
	if err != nil {
		return nil, nil, err
	}
	// MultiWriter also surfaces logs on stderr in development so the
	// operator sees lifecycle lines without tailing the file.
	w := io.MultiWriter(rw, os.Stderr)
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: false,
	})
	return slog.New(h), rw, nil
}

// buildMux wires every API endpoint onto a fresh ServeMux. Kept as a
// function so tests can construct a mux against a throwaway DB.
func buildMux(db *DB, logger *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	// Health / version probe.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"version": Version,
			"time":    time.Now().UTC().Format(time.RFC3339Nano),
		})
	})

	// Active registration from a running Stoke instance.
	mux.HandleFunc("POST /api/register", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var sig session.SignatureFile
		if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&sig); err != nil {
			writeErr(w, http.StatusBadRequest, "decode signature: %v", err)
			return
		}
		if sig.InstanceID == "" {
			writeErr(w, http.StatusBadRequest, "missing instance_id")
			return
		}
		if err := db.UpsertSession(sig); err != nil {
			logger.Error("upsert session", "instance_id", sig.InstanceID, "err", err)
			writeErr(w, http.StatusInternalServerError, "upsert: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// List sessions (optional ?status= filter).
	//
	// Content-negotiated: Accept: text/html (htmx default) returns a
	// grid of session_row.tmpl partials wrapped in the htmx polling
	// container -- see index.go's serveSessionsPartial. Everything
	// else gets the original JSON body so API consumers are
	// unaffected by the v2 UI retrofit (work-stoke TASK 12).
	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		if indexV2Enabled() && wantsHTML(r) {
			db.serveSessionsPartial(w, r)
			return
		}
		status := r.URL.Query().Get("status")
		rows, err := db.ListSessions(status)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "list sessions: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sessions": rows,
			"count":    len(rows),
		})
	})

	// Cross-session SSE firehose (work-stoke TASK 12). The htmx
	// index layout subscribes here via hx-ext="sse" +
	// sse-connect="/api/events". See index.go's
	// handleAllEventsStream for framing details. Unlike the
	// per-session /api/session/{id}/events/stream this endpoint
	// renders an HTML fragment per event so htmx's sse-swap can
	// insert the row verbatim without a client-side formatter.
	mux.HandleFunc("GET /api/events", handleAllEventsStream(db, logger))

	// Per-session signature + metadata.
	mux.HandleFunc("GET /api/session/{id}", func(w http.ResponseWriter, r *http.Request) {
		row, err := db.GetSession(r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusNotFound, "session not found: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, row)
	})

	// Live-tailing SSE stream for a session (RS-4 item 19). Registered
	// alongside the /events route — Go 1.22's pattern precedence picks
	// the more specific path (/events/stream) over the less specific
	// one (/events), so we can declare them in either order. We keep
	// this before /events for readability: EventSource first, paginated
	// fallback second.
	mux.HandleFunc("GET /api/session/{id}/events/stream", handleEventsStream(db, logger))

	// Cursor-paginated events for a session.
	mux.HandleFunc("GET /api/session/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := db.ListEvents(id, after, limit)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "list events: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"instance_id": id,
			"after":       after,
			"events":      rows,
			"count":       len(rows),
		})
	})

	// Full ledger DAG for a session (nodes + edges).
	mux.HandleFunc("GET /api/session/{id}/ledger", func(w http.ResponseWriter, r *http.Request) {
		snap, err := db.GetLedger(r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "load ledger: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, snap)
	})

	// Checkpoint timeline (JSONL tail from the signature's checkpoint_file).
	mux.HandleFunc("GET /api/session/{id}/checkpoints", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		row, err := db.GetSession(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "session not found: %v", err)
			return
		}
		entries, err := readCheckpointFile(row.CheckpointFile)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read checkpoints: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"instance_id": id,
			"file":        row.CheckpointFile,
			"entries":     entries,
			"count":       len(entries),
		})
	})

	return mux
}

// readCheckpointFile slurps a checkpoint JSONL file and returns each
// non-empty line as a parsed JSON object. Missing file is treated as
// empty, not an error — sessions that haven't written checkpoints
// yet shouldn't 500 the endpoint.
func readCheckpointFile(path string) ([]json.RawMessage, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []json.RawMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		entry := make(json.RawMessage, len(line))
		copy(entry, line)
		out = append(out, entry)
	}
	return out, sc.Err()
}

// writeJSON marshals v as indented JSON and writes it with the given
// status. Marshaling errors fall back to a plain-text 500 so callers
// never see a half-written body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "marshal error: %v\n", err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(body)
	w.Write([]byte("\n"))
}

// writeErr emits a JSON error body with the given status and message.
func writeErr(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]any{
		"error": fmt.Sprintf(format, args...),
	})
}

// loggingMiddleware wraps a handler with slog access logs. Response
// status is captured via a wrapper so we log the actual outcome.
func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"bytes", sw.written,
			"dur_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
	}
	n, err := s.ResponseWriter.Write(b)
	s.written += int64(n)
	return n, err
}

// rotatingWriter is a minimal size-capped file writer. When Write
// would push the file past maxBytes, the current file is renamed to
// <path>.1 (replacing any prior .1) and a fresh file is opened.
// Sufficient for r1-server's low-volume logs; not a replacement for
// lumberjack if high-volume rotation is ever needed.
type rotatingWriter struct {
	path     string
	maxBytes int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

func newRotatingWriter(path string, maxBytes int64) (*rotatingWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &rotatingWriter{path: path, maxBytes: maxBytes, f: f, size: info.Size()}, nil
}

func (r *rotatingWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingWriter) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	return r.f.Close()
}

func (r *rotatingWriter) rotateLocked() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	_ = os.Rename(r.path, r.path+".1")
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	r.f = f
	r.size = 0
	return nil
}
