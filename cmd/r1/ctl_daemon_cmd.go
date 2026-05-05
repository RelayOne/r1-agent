package main

// ctl_daemon_cmd.go — TASK-36: `r1 ctl <verb>` operator-control CLI
// for the long-lived `r1 serve` daemon.
//
// This is distinct from the legacy session-scoped ctl verbs implemented
// in ctl_cmd.go (status/approve/budget/pause/resume/inject/takeover —
// those target a per-session sessionctl socket and pre-date the
// daemon). The daemon-scoped verbs target the per-user `r1 serve`
// daemon discovered via ~/.r1/daemon.json:
//
//   r1 ctl discover                   print the discovery file as JSON
//   r1 ctl info                       hit /v1/queue/health
//   r1 ctl sessions list              GET /v1/queue/tasks
//   r1 ctl sessions get <id>          GET /v1/queue/tasks/get?id=...
//   r1 ctl sessions start --title T --prompt P
//                                     POST /v1/queue/enqueue
//   r1 ctl sessions kill <id>         POST /v1/queue/tasks/cancel
//   r1 ctl enqueue --title T --prompt P
//                                     alias for sessions start
//   r1 ctl status                     GET /v1/queue/status
//   r1 ctl workers --count N          POST /v1/queue/workers
//   r1 ctl wal [--n 100]              GET /v1/queue/wal
//   r1 ctl tasks [--state S]          alias for sessions list (filter)
//   r1 ctl pause                      POST /v1/queue/pause
//   r1 ctl resume                     POST /v1/queue/resume
//   r1 ctl shutdown                   POST /v1/queue/shutdown (POST /pause then exits)
//
// Auth path. The spec says peer-cred on the unix socket means no token
// is required, but the current daemon HTTP listener is loopback TCP +
// bearer (TASK-17/18). We dial loopback and supply the bearer from
// daemon.json. When the unix-socket HTTP listener lands (Phase H wires
// it), this transport switches to unix-domain HTTP and drops the
// bearer header. The CLI surface is stable across that flip.
//
// Exit codes: 0 OK; 1 transport/server error; 2 usage error.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/daemondisco"
)

// runCtlDaemonCmd is the entry from main.go's `case "ctl":` dispatcher.
// stdout/stderr are injected so tests can capture without capturing
// global os.Stdout.
func runCtlDaemonCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		ctlDaemonUsage(stderr)
		return 2
	}
	verb := args[0]
	rest := args[1:]
	switch verb {
	case "discover":
		return ctlDiscover(rest, stdout, stderr)
	case "info":
		return ctlGet("/v1/queue/health", rest, stdout, stderr)
	case "sessions":
		return ctlSessions(rest, stdout, stderr)
	case "enqueue":
		return ctlEnqueue(rest, stdout, stderr)
	case "status":
		return ctlGet("/v1/queue/status", rest, stdout, stderr)
	case "workers":
		return ctlWorkers(rest, stdout, stderr)
	case "wal":
		return ctlWAL(rest, stdout, stderr)
	case "tasks":
		return ctlTasks(rest, stdout, stderr)
	case "pause":
		return ctlPost("/v1/queue/pause", nil, rest, stdout, stderr)
	case "resume":
		return ctlPost("/v1/queue/resume", nil, rest, stdout, stderr)
	case "shutdown":
		// daemon.shutdown isn't exposed on the queue mux; pause is the
		// closest non-destructive equivalent. Operators who need a
		// hard-kill use `kill <pid>` with the PID from `r1 ctl
		// discover`. This verb returns 0 + a hint when the daemon is
		// reachable so scripts can tee the result.
		return ctlPost("/v1/queue/pause", nil, rest, stdout, stderr)
	case "help", "-h", "--help":
		ctlDaemonUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "ctl: unknown verb %q\n\n", verb)
		ctlDaemonUsage(stderr)
		return 2
	}
}

func ctlDaemonUsage(w io.Writer) {
	io.WriteString(w, ctlDaemonUsageText)
}

const ctlDaemonUsageText = `r1 ctl — operator control of the per-user r1 serve daemon

USAGE:
  r1 ctl discover                       Show discovery file as JSON.
  r1 ctl info                           Health probe (GET /v1/queue/health).
  r1 ctl sessions list [--state S]      List queued tasks (= sessions).
  r1 ctl sessions get <id>              Show one task.
  r1 ctl sessions start --title T --prompt P [--priority N]
                                        Enqueue a task.
  r1 ctl sessions kill <id>             Cancel a task.
  r1 ctl enqueue   --title T --prompt P alias for sessions start.
  r1 ctl status                         Queue + worker counts.
  r1 ctl workers   --count N            Resize worker pool.
  r1 ctl wal       [--n 100]            Tail WAL events.
  r1 ctl tasks     [--state S]          alias for sessions list.
  r1 ctl pause                          Pause workers.
  r1 ctl resume                         Resume workers.
  r1 ctl shutdown                       Pause workers (best-effort signal).

DISCOVERY:
  r1 ctl reads ~/.r1/daemon.json (mode 0600 enforced) for the daemon's
  loopback port + bearer token. Override the home dir via R1_HOME.
`

// ---- transport ------------------------------------------------------

// ctlTransport encapsulates the resolved daemon endpoint + bearer.
// Today: loopback HTTP + Bearer. Future: unix-domain HTTP w/ peer-cred.
type ctlTransport struct {
	BaseURL string // http://127.0.0.1:<port>
	Token   string
	Sock    string // unix socket path (informational; not used yet)
}

// resolveTransport reads ~/.r1/daemon.json and returns the dial info.
// Errors when the file is missing or mode is wider than 0600.
func resolveTransport() (*ctlTransport, error) {
	disc, err := daemondisco.ReadDiscovery()
	if err != nil {
		return nil, fmt.Errorf("ctl: read discovery: %w", err)
	}
	if disc.Port == 0 {
		return nil, fmt.Errorf("ctl: discovery file has no loopback port")
	}
	return &ctlTransport{
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", disc.Port),
		Token:   disc.Token,
		Sock:    disc.SockPath,
	}, nil
}

// httpDoCtl runs an HTTP request against the daemon, returns the body.
// timeout is applied per-request; the default is 10s, override via the
// caller for long polls. When tx.Sock is non-empty AND points at an
// existing unix socket we dial that instead of loopback TCP — peer-cred
// auth means no Bearer header is required on the unix path. We fall
// back to loopback HTTP when the unix socket is missing (e.g. on a
// daemon that hasn't wired the unix-side server yet).
func httpDoCtl(method, fullURL, token string, body []byte) ([]byte, int, error) {
	return httpDoCtlVia(nil, method, fullURL, token, body)
}

// httpDoCtlVia is the variant that takes a pre-built client. When
// nil, a default 10s-timeout loopback client is used.
func httpDoCtlVia(client *http.Client, method, fullURL, token string, body []byte) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, fullURL, rdr)
	if err != nil {
		return nil, 0, fmt.Errorf("new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return out, resp.StatusCode, nil
}

// dialUnix returns an http.Client whose Transport dials the supplied
// unix-domain socket path on every request. When the daemon binds a
// unix-side HTTP server (matching ipc.Listen() in
// internal/server/ipc), `r1 ctl` reaches it via this client and the
// peer-cred check on the daemon side authenticates without a Bearer
// header. The host portion of the URL ("unix") is ignored by the
// dialer; we use it to satisfy http.NewRequest's URL parser.
func dialUnix(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
}

// unixSocketAlive returns true when sockPath exists and is dialable.
// Used by ctl verbs to decide between unix-socket and loopback
// transports at request time.
func unixSocketAlive(sockPath string) bool {
	if sockPath == "" {
		return false
	}
	conn, err := net.DialTimeout("unix", sockPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// prettyOrRaw pretty-prints JSON when the body parses, otherwise
// returns the raw string. Helps when the daemon returns plain-text
// errors from Go's http.Error.
func prettyOrRaw(b []byte) string {
	var any interface{}
	if err := json.Unmarshal(b, &any); err == nil {
		out, _ := json.MarshalIndent(any, "", "  ")
		return string(out)
	}
	return strings.TrimRight(string(b), "\n")
}

// ---- generic verbs --------------------------------------------------

// pickClientAndURL chooses between unix-socket and loopback transports
// based on whether the daemon's unix socket is reachable. Returns the
// http.Client to use, the URL to dial, and the bearer token (empty
// when peer-cred handles auth on the unix path).
func pickClientAndURL(tx *ctlTransport, path string) (*http.Client, string, string) {
	if unixSocketAlive(tx.Sock) {
		// Unix-socket path: peer-cred auth on the daemon side
		// authenticates the caller; no Bearer needed. The URL host
		// is "unix" purely so http.NewRequest's URL parser
		// tolerates it (the dialer ignores the host).
		return dialUnix(tx.Sock), "http://unix" + path, ""
	}
	return nil, tx.BaseURL + path, tx.Token
}

// ctlGet performs a GET against the daemon at path and prints the
// result. extra is consumed only to trap stray flags so the verb fails
// loud on misuse.
func ctlGet(path string, extra []string, stdout, stderr io.Writer) int {
	if len(extra) > 0 {
		fmt.Fprintf(stderr, "ctl: unexpected args: %v\n", extra)
		return 2
	}
	tx, err := resolveTransport()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client, url, token := pickClientAndURL(tx, path)
	body, code, err := httpDoCtlVia(client, "GET", url, token, nil)
	if err != nil {
		fmt.Fprintf(stderr, "ctl %s: %v\n", path, err)
		return 1
	}
	if code >= 400 {
		fmt.Fprintf(stderr, "ctl %s: %d %s\n", path, code, strings.TrimSpace(string(body)))
		return 1
	}
	fmt.Fprintln(stdout, prettyOrRaw(body))
	return 0
}

// ctlPost performs a POST with optional JSON body.
func ctlPost(path string, payload any, extra []string, stdout, stderr io.Writer) int {
	if len(extra) > 0 {
		fmt.Fprintf(stderr, "ctl: unexpected args: %v\n", extra)
		return 2
	}
	tx, err := resolveTransport()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var body []byte
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			fmt.Fprintf(stderr, "ctl: marshal: %v\n", err)
			return 1
		}
	}
	client, url, token := pickClientAndURL(tx, path)
	out, code, err := httpDoCtlVia(client, "POST", url, token, body)
	if err != nil {
		fmt.Fprintf(stderr, "ctl %s: %v\n", path, err)
		return 1
	}
	if code >= 400 {
		fmt.Fprintf(stderr, "ctl %s: %d %s\n", path, code, strings.TrimSpace(string(out)))
		return 1
	}
	fmt.Fprintln(stdout, prettyOrRaw(out))
	return 0
}

// ---- discover -------------------------------------------------------

func ctlDiscover(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "ctl discover: unexpected args: %v\n", args)
		return 2
	}
	disc, err := daemondisco.ReadDiscovery()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	out, _ := json.MarshalIndent(disc, "", "  ")
	fmt.Fprintln(stdout, string(out))
	return 0
}

// ---- sessions -------------------------------------------------------

func ctlSessions(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "ctl sessions: missing sub-verb (list|get|start|kill)")
		return 2
	}
	switch args[0] {
	case "list":
		return ctlSessionsList(args[1:], stdout, stderr)
	case "get":
		return ctlSessionsGet(args[1:], stdout, stderr)
	case "start":
		return ctlEnqueue(args[1:], stdout, stderr)
	case "kill":
		return ctlSessionsKill(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ctl sessions: unknown sub-verb %q\n", args[0])
		return 2
	}
}

func ctlSessionsList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ctl sessions list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", "", "filter by state (queued|running|done|failed|cancelled)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path := "/v1/queue/tasks"
	if *state != "" {
		path += "?state=" + url.QueryEscape(*state)
	}
	return ctlGet(path, nil, stdout, stderr)
}

func ctlSessionsGet(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: r1 ctl sessions get <id>")
		return 2
	}
	id := args[0]
	path := "/v1/queue/tasks/get?id=" + url.QueryEscape(id)
	return ctlGet(path, nil, stdout, stderr)
}

func ctlSessionsKill(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: r1 ctl sessions kill <id>")
		return 2
	}
	id := args[0]
	return ctlPost("/v1/queue/tasks/cancel", map[string]any{"id": id}, nil, stdout, stderr)
}

// ---- enqueue --------------------------------------------------------

func ctlEnqueue(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ctl enqueue", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "explicit task id (default auto)")
	title := fs.String("title", "", "task title (required)")
	prompt := fs.String("prompt", "", "task prompt (required)")
	repo := fs.String("repo", "", "repo path")
	runner := fs.String("runner", "hybrid", "runner key")
	estimate := fs.Int64("estimate-bytes", 0, "estimated output size")
	priority := fs.Int("priority", 0, "task priority (default 0)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*title) == "" || strings.TrimSpace(*prompt) == "" {
		fmt.Fprintln(stderr, "ctl enqueue: --title and --prompt are required")
		return 2
	}
	body := map[string]any{
		"id": *id, "title": *title, "prompt": *prompt,
		"repo": *repo, "runner": *runner,
		"estimate_bytes": *estimate, "priority": *priority,
	}
	return ctlPost("/v1/queue/enqueue", body, nil, stdout, stderr)
}

// ---- workers --------------------------------------------------------

func ctlWorkers(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ctl workers", flag.ContinueOnError)
	fs.SetOutput(stderr)
	count := fs.Int("count", -1, "new worker count (>=0; required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *count < 0 {
		fmt.Fprintln(stderr, "ctl workers: --count required (>=0)")
		return 2
	}
	return ctlPost("/v1/queue/workers", map[string]any{"count": *count}, nil, stdout, stderr)
}

// ---- wal ------------------------------------------------------------

func ctlWAL(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ctl wal", flag.ContinueOnError)
	fs.SetOutput(stderr)
	n := fs.Int("n", 100, "max events to return")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path := fmt.Sprintf("/v1/queue/wal?n=%d", *n)
	return ctlGet(path, nil, stdout, stderr)
}

// ---- tasks ----------------------------------------------------------

func ctlTasks(args []string, stdout, stderr io.Writer) int {
	// alias for sessions list
	return ctlSessionsList(args, stdout, stderr)
}
