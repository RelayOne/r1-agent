// stoke-server — single-instance background process that discovers
// live stoke instances via runtrack manifests and serves a web
// dashboard on port 3948 showing every active + recent run, with
// deep links into their JSONL worker logs.
//
// Singleton: on startup we try to bind :3948. If the bind fails with
// "address already in use", we assume another stoke-server is running
// and exit 0 silently — `stoke sow` auto-launches us, so repeated
// "already running" exits are expected and harmless.
//
// Data model: purely filesystem today. Manifests live in
// /tmp/stoke/instances/<run_id>.json; JSONL logs live at paths the
// manifest points to. No SQLite yet (Phase 2). On every request
// the server re-reads the instance directory + the relevant JSONL
// files, which is fast for a few dozen runs and avoids consistency
// headaches.
//
// Endpoints:
//
//	GET /              — HTML dashboard (instance list + controls)
//	GET /api/runs      — JSON [{manifest, alive, summary}, ...]
//	GET /api/run/{id}  — JSON {manifest, alive, jsonl_tail}
//	GET /run/{id}      — HTML JSONL viewer for one run
//	GET /tail/{id}     — text/plain tail of the JSONL (for curl)
//	GET /healthz       — "ok"
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ericmacdougall/stoke/internal/runtrack"
)

const defaultPort = 3948

func main() {
	port := flag.Int("port", defaultPort, "HTTP port")
	flag.Parse()

	addr := fmt.Sprintf(":%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Singleton behavior: silently exit if another server is bound.
		if isAddrInUse(err) {
			fmt.Fprintf(os.Stderr, "stoke-server: another instance already bound to %s — exiting silently\n", addr)
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "stoke-server: listen %s: %v\n", addr, err)
		os.Exit(2)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/api/runs", handleAPIRuns)
	mux.HandleFunc("/api/run/", handleAPIRun)
	mux.HandleFunc("/tail/", handleTail)
	mux.HandleFunc("/run/", handleRunHTML)
	mux.HandleFunc("/", handleRoot)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("stoke-server listening on %s (instances dir: %s)\n", addr, runtrack.InstancesDir())
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "stoke-server: serve: %v\n", err)
		}
	}()

	<-ctx.Done()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
}

func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if s := opErr.Err.Error(); strings.Contains(s, "address already in use") {
			return true
		}
	}
	return strings.Contains(err.Error(), "address already in use")
}

// --- handlers ---

type runView struct {
	Manifest   runtrack.Manifest `json:"manifest"`
	Alive      bool              `json:"alive"`
	AgeSeconds int64             `json:"age_seconds"` // since heartbeat
	ToolCount  int               `json:"tool_count"`  // from tail scan
}

func loadRuns() ([]runView, error) {
	ms, err := runtrack.List()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]runView, 0, len(ms))
	for _, m := range ms {
		rv := runView{Manifest: m}
		rv.Alive = runtrack.IsProcessAlive(m.PID)
		if t, err := time.Parse(time.RFC3339Nano, m.Heartbeat); err == nil {
			rv.AgeSeconds = int64(now.Sub(t).Seconds())
		}
		rv.ToolCount = countToolCallsForRun(m.WorkerLogsDir)
		out = append(out, rv)
	}
	sort.Slice(out, func(i, j int) bool {
		// Alive first, then newest-first by StartedAt.
		if out[i].Alive != out[j].Alive {
			return out[i].Alive
		}
		return out[i].Manifest.StartedAt > out[j].Manifest.StartedAt
	})
	return out, nil
}

// countToolCallsForRun returns the total number of JSONL lines
// across every worker log under logsDir. Fast filesystem scan;
// no parsing. 0 when dir missing (common for freshly-started runs).
func countToolCallsForRun(logsDir string) int {
	if logsDir == "" {
		return 0
	}
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return 0
	}
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Cheap estimate: avg line ~200 bytes. Good enough for "busy vs idle".
		total += int(info.Size() / 200)
	}
	return total
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func handleAPIRuns(w http.ResponseWriter, _ *http.Request) {
	runs, err := loadRuns()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runs)
}

func handleAPIRun(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/run/")
	if id == "" {
		http.Error(w, "missing run id", http.StatusBadRequest)
		return
	}
	m, found := findManifest(id)
	if !found {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	out := map[string]any{
		"manifest":    m,
		"alive":       runtrack.IsProcessAlive(m.PID),
		"jsonl_files": listJSONLFiles(m.WorkerLogsDir),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func handleTail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/tail/")
	if id == "" {
		http.Error(w, "missing run id", http.StatusBadRequest)
		return
	}
	m, found := findManifest(id)
	if !found {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	files := listJSONLFiles(m.WorkerLogsDir)
	for _, f := range files {
		fmt.Fprintf(w, "=== %s ===\n", filepath.Base(f))
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(w, "(read error: %v)\n", err)
			continue
		}
		// Cap each file's dump at 256KB so a runaway log doesn't blow the tab.
		if len(data) > 256*1024 {
			fmt.Fprintf(w, "(truncated — first 256KB of %d bytes)\n", len(data))
			data = data[:256*1024]
		}
		_, _ = w.Write(data)
		_, _ = w.Write([]byte("\n"))
	}
}

func handleRunHTML(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/run/")
	if id == "" {
		http.Error(w, "missing run id", http.StatusBadRequest)
		return
	}
	m, found := findManifest(id)
	if !found {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = runTmpl.Execute(w, map[string]any{
		"RunID":     id,
		"Manifest":  m,
		"Alive":     runtrack.IsProcessAlive(m.PID),
		"JSONLFiles": listJSONLFiles(m.WorkerLogsDir),
	})
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	runs, err := loadRuns()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTmpl.Execute(w, map[string]any{
		"Runs": runs,
		"Now":  time.Now().UTC().Format(time.RFC3339),
		"Dir":  runtrack.InstancesDir(),
	})
}

// --- helpers ---

func findManifest(id string) (runtrack.Manifest, bool) {
	ms, _ := runtrack.List()
	for _, m := range ms {
		if m.RunID == id {
			return m, true
		}
	}
	return runtrack.Manifest{}, false
}

func listJSONLFiles(dir string) []string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out
}

// --- templates ---

var funcMap = template.FuncMap{
	"humanBytes": func(n int64) string {
		switch {
		case n < 1024:
			return strconv.FormatInt(n, 10) + "B"
		case n < 1024*1024:
			return fmt.Sprintf("%.1fKB", float64(n)/1024)
		default:
			return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
		}
	},
	"baseName": func(s string) string { return filepath.Base(s) },
}

var indexTmpl = template.Must(template.New("index").Funcs(funcMap).Parse(`<!doctype html>
<html><head><meta charset="utf-8">
<title>stoke-server</title>
<style>
body{font:14px/1.4 system-ui,sans-serif;margin:0;background:#0a0a0a;color:#d0d0d0}
header{padding:16px 24px;background:#111;border-bottom:1px solid #222;display:flex;gap:16px;align-items:baseline}
h1{margin:0;font-size:18px;font-weight:600;color:#fff}
.meta{color:#888;font-size:12px}
main{padding:24px}
table{width:100%;border-collapse:collapse}
th{text-align:left;font-weight:600;padding:8px 12px;color:#888;border-bottom:1px solid #222;font-size:12px;text-transform:uppercase;letter-spacing:0.5px}
td{padding:10px 12px;border-bottom:1px solid #1a1a1a;vertical-align:top}
tr.alive td{background:#0d1512}
.pill{display:inline-block;padding:2px 8px;border-radius:10px;font-size:11px;font-weight:600}
.alive{background:#0d3a24;color:#4ade80}
.dead{background:#2a1515;color:#f87171}
a{color:#60a5fa;text-decoration:none}
a:hover{text-decoration:underline}
.empty{text-align:center;padding:48px;color:#666}
code{font:12px ui-monospace,monospace;color:#a3a3a3}
</style></head>
<body>
<header>
<h1>stoke-server</h1>
<span class="meta">{{.Dir}} · {{.Now}}</span>
</header>
<main>
{{if .Runs}}
<table>
<thead><tr>
<th>Status</th><th>Run</th><th>Command</th><th>Repo</th><th>Model</th><th>Build</th><th>Age</th><th>Tools</th>
</tr></thead>
<tbody>
{{range .Runs}}
<tr{{if .Alive}} class="alive"{{end}}>
<td>{{if .Alive}}<span class="pill alive">ACTIVE</span>{{else}}<span class="pill dead">dead</span>{{end}}</td>
<td><a href="/run/{{.Manifest.RunID}}"><code>{{.Manifest.RunID}}</code></a><br><span class="meta">pid {{.Manifest.PID}}</span></td>
<td>{{.Manifest.Command}}<br><span class="meta">{{.Manifest.SOWName}}</span></td>
<td><code>{{.Manifest.RepoRoot}}</code></td>
<td>{{.Manifest.Model}}</td>
<td><code>{{.Manifest.StokeBuild}}</code></td>
<td>{{.AgeSeconds}}s</td>
<td>{{.ToolCount}}</td>
</tr>
{{end}}
</tbody>
</table>
{{else}}
<div class="empty">No stoke instances registered yet.<br><span class="meta">Start one with <code>stoke sow</code> and it will appear here.</span></div>
{{end}}
</main>
</body></html>`))

var runTmpl = template.Must(template.New("run").Funcs(funcMap).Parse(`<!doctype html>
<html><head><meta charset="utf-8">
<title>stoke run · {{.RunID}}</title>
<style>
body{font:13px/1.5 ui-monospace,monospace;margin:0;background:#0a0a0a;color:#d0d0d0}
header{padding:12px 20px;background:#111;border-bottom:1px solid #222}
header h1{margin:0;font-size:16px;color:#fff;font-family:system-ui}
header .meta{color:#888;font-size:12px;margin-top:4px}
main{padding:16px 20px}
dl{display:grid;grid-template-columns:max-content 1fr;gap:6px 16px;margin:0 0 16px;font-size:12px}
dt{color:#888}
dd{margin:0;color:#d0d0d0}
.file{margin:16px 0;border:1px solid #222;border-radius:6px;overflow:hidden}
.file-head{background:#151515;padding:8px 12px;font-weight:600;color:#fff;font-size:12px;display:flex;justify-content:space-between;align-items:center}
.file-head a{color:#60a5fa;text-decoration:none}
pre{margin:0;padding:12px;overflow:auto;max-height:480px;font-size:11px;background:#0a0a0a;color:#c0c0c0;white-space:pre}
</style></head>
<body>
<header>
<h1>{{.Manifest.SOWName}} {{if .Alive}}<span style="color:#4ade80">●</span>{{else}}<span style="color:#f87171">●</span>{{end}}</h1>
<div class="meta">{{.RunID}} · pid {{.Manifest.PID}} · <a href="/">← back</a></div>
</header>
<main>
<dl>
<dt>Command</dt><dd>{{.Manifest.Command}}</dd>
<dt>Args</dt><dd>{{.Manifest.Args}}</dd>
<dt>Repo</dt><dd>{{.Manifest.RepoRoot}}</dd>
<dt>Mode</dt><dd>{{.Manifest.Mode}}</dd>
<dt>Model</dt><dd>{{.Manifest.Model}}</dd>
<dt>Build</dt><dd>{{.Manifest.StokeBuild}}</dd>
<dt>Started</dt><dd>{{.Manifest.StartedAt}}</dd>
<dt>Last HB</dt><dd>{{.Manifest.Heartbeat}}</dd>
<dt>Log dir</dt><dd>{{.Manifest.WorkerLogsDir}}</dd>
</dl>

{{range .JSONLFiles}}
<div class="file">
  <div class="file-head">{{baseName .}} <a href="/tail/{{$.RunID}}" target="_blank">full tail →</a></div>
  <pre id="pre-{{baseName .}}" data-path="{{.}}">loading…</pre>
</div>
{{end}}

<script>
// Simple fetch-and-render for each JSONL file. Inline — no external deps.
document.querySelectorAll('pre[data-path]').forEach(async pre => {
  const r = await fetch('/tail/{{.RunID}}', {headers: {'Accept': 'text/plain'}});
  const text = await r.text();
  // Split by === markers and render only this file's section.
  const name = pre.previousElementSibling ? pre.previousElementSibling.textContent : '';
  const marker = '=== ' + pre.id.replace('pre-','') + ' ===';
  const parts = text.split(marker);
  pre.textContent = parts.length > 1 ? parts[1].split(/\n=== /)[0].trim() : text.slice(0, 8192);
});
</script>

</main>
</body></html>`))
