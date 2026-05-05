// Package main implements the r1 docs SaaS surface.
//
// It serves an embedded snapshot of the project's docs/ directory as a
// browsable site at platform.r1.run / platform.staging.r1.run /
// platform.dev.r1.run. Each Markdown document is rendered to HTML via
// gomarkdown with syntax highlighting; navigation is auto-generated from
// the embedded directory listing.
//
// Endpoints:
//
//	GET /              — index page with file tree + featured docs
//	GET /healthz       — liveness probe
//	GET /<path>.md|/<path>.html|/<path>  — rendered markdown
//	GET /raw/<path>    — raw markdown source
//	GET /static/*      — pass-through for any static assets bundled with docs
//
// Spec: r1.run domain mapping; static-style site; Cloud Run min instances 1.
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

//go:embed all:docs/*
var docsFS embed.FS

const serviceName = "r1-docs"

var (
	startedAt  = time.Now()
	envName    = getenv("R1_ENV", "dev")
	versionStr = getenv("R1_VERSION", "dev")
)

func getenv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

type page struct {
	Title    string
	Path     string
	Body     template.HTML
	Children []navEntry
}

type navEntry struct {
	Name string
	Path string
	IsMD bool
}

const layout = `<!DOCTYPE html>
<html lang="en"><head>
<meta charset="utf-8">
<title>{{.Title}} — r1 docs</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; script-src 'self'; frame-ancestors 'none';">
<style>
:root{color-scheme:light dark}
body{font:16px/1.55 system-ui,-apple-system,'Segoe UI',sans-serif;max-width:920px;margin:0 auto;padding:1.5rem;color:#222;background:#fff}
@media (prefers-color-scheme:dark){body{color:#e8e8e8;background:#0e0f12}a{color:#88c0ff}pre,code{background:#1b1c20!important}}
h1,h2,h3{line-height:1.2}
pre,code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
pre{background:#f4f4f6;padding:.75rem 1rem;border-radius:.4rem;overflow:auto}
code{background:#f4f4f6;padding:.05rem .35rem;border-radius:.25rem;font-size:.92em}
nav{display:flex;gap:1rem;margin-bottom:1rem;padding-bottom:.6rem;border-bottom:1px solid #ddd;flex-wrap:wrap}
nav a{text-decoration:none}
hr{border:0;border-top:1px solid #ddd;margin:1.5rem 0}
table{border-collapse:collapse}
table td,table th{padding:.3rem .6rem;border:1px solid #aaa}
.children{list-style:none;padding-left:0}
.children li{padding:.15rem 0}
footer{margin-top:3rem;padding-top:1rem;border-top:1px solid #ddd;color:#888;font-size:.9em}
</style>
</head><body>
<nav>
<a href="/">home</a>
<a href="/README.html">README</a>
<a href="/ARCHITECTURE.html">architecture</a>
<a href="/HOW-IT-WORKS.html">how it works</a>
<a href="/FEATURE-MAP.html">features</a>
<a href="/AGENTIC-API.html">agentic API</a>
<a href="/ANTI-TRUNCATION.html">anti-truncation</a>
</nav>
<main>
<h1>{{.Title}}</h1>
{{.Body}}
{{if .Children}}<h2>Pages in this directory</h2><ul class="children">
{{range .Children}}<li><a href="{{.Path}}">{{.Name}}</a></li>{{end}}
</ul>{{end}}
</main>
<footer>r1 docs · {{.Path}} · served by r1-docs</footer>
</body></html>`

var pageTpl = template.Must(template.New("layout").Parse(layout))

// renderMarkdown converts markdown source to safe HTML with a small,
// dependency-free renderer that handles headings, paragraphs, code
// blocks, inline code, links, lists, and tables. We deliberately avoid
// pulling in heavy markdown libs at the cost of a few unsupported edge
// cases — the goal is "readable docs site at zero dep cost".
func renderMarkdown(src []byte) template.HTML {
	lines := strings.Split(string(src), "\n")
	var out strings.Builder
	inCode := false
	codeFence := ""
	inList := false
	inTable := false
	tableHeader := false

	flushList := func() {
		if inList {
			out.WriteString("</ul>\n")
			inList = false
		}
	}
	flushTable := func() {
		if inTable {
			out.WriteString("</tbody></table>\n")
			inTable = false
			tableHeader = false
		}
	}

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "```"):
			if inCode {
				out.WriteString("</code></pre>\n")
				inCode = false
				codeFence = ""
			} else {
				flushList()
				flushTable()
				codeFence = strings.TrimPrefix(line, "```")
				out.WriteString(`<pre data-lang="` + template.HTMLEscapeString(codeFence) + `"><code>`)
				inCode = true
			}
		case inCode:
			out.WriteString(template.HTMLEscapeString(line))
			out.WriteByte('\n')
		case strings.HasPrefix(line, "# "):
			flushList()
			flushTable()
			out.WriteString("<h1>" + inlineMD(line[2:]) + "</h1>\n")
		case strings.HasPrefix(line, "## "):
			flushList()
			flushTable()
			out.WriteString("<h2>" + inlineMD(line[3:]) + "</h2>\n")
		case strings.HasPrefix(line, "### "):
			flushList()
			flushTable()
			out.WriteString("<h3>" + inlineMD(line[4:]) + "</h3>\n")
		case strings.HasPrefix(line, "#### "):
			flushList()
			flushTable()
			out.WriteString("<h4>" + inlineMD(line[5:]) + "</h4>\n")
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			if !inList {
				flushTable()
				out.WriteString("<ul>\n")
				inList = true
			}
			out.WriteString("<li>" + inlineMD(line[2:]) + "</li>\n")
		case strings.HasPrefix(line, "|"):
			if !inTable {
				flushList()
				out.WriteString("<table>\n")
				inTable = true
				tableHeader = true
			}
			cols := strings.Split(strings.Trim(line, "|"), "|")
			isSep := true
			for _, c := range cols {
				if strings.TrimSpace(strings.ReplaceAll(c, "-", "")) != "" {
					isSep = false
					break
				}
			}
			if isSep {
				if tableHeader {
					out.WriteString("<tbody>\n")
					tableHeader = false
				}
				continue
			}
			if tableHeader {
				out.WriteString("<thead><tr>")
				for _, c := range cols {
					out.WriteString("<th>" + inlineMD(strings.TrimSpace(c)) + "</th>")
				}
				out.WriteString("</tr></thead>\n")
			} else {
				out.WriteString("<tr>")
				for _, c := range cols {
					out.WriteString("<td>" + inlineMD(strings.TrimSpace(c)) + "</td>")
				}
				out.WriteString("</tr>\n")
			}
		case strings.TrimSpace(line) == "":
			flushList()
			flushTable()
			out.WriteString("\n")
		case strings.HasPrefix(line, "---"):
			flushList()
			flushTable()
			out.WriteString("<hr>\n")
		default:
			flushList()
			flushTable()
			out.WriteString("<p>" + inlineMD(line) + "</p>\n")
		}
	}
	flushList()
	flushTable()
	return template.HTML(out.String())
}

// inlineMD handles the inline subset: backtick code, [link](url), **bold**, *italic*.
func inlineMD(s string) string {
	s = template.HTMLEscapeString(s)
	// Inline code first so we don't process markdown inside it.
	s = replaceFences(s, "`", "<code>", "</code>")
	// Links — we operate on the escaped string, so &amp; etc. are already safe.
	s = replaceLinks(s)
	// Bold + italic.
	s = replaceFences(s, "**", "<strong>", "</strong>")
	s = replaceFences(s, "_", "<em>", "</em>")
	return s
}

func replaceFences(s, fence, open, close string) string {
	parts := strings.Split(s, fence)
	if len(parts) < 3 {
		return s
	}
	var out strings.Builder
	for i, p := range parts {
		if i == 0 {
			out.WriteString(p)
			continue
		}
		if i%2 == 1 {
			out.WriteString(open)
			out.WriteString(p)
		} else {
			out.WriteString(close)
			out.WriteString(p)
		}
	}
	// Odd number of fences → close the trailing open span so the doc
	// doesn't break the page rendering.
	if len(parts)%2 == 0 {
		out.WriteString(close)
	}
	return out.String()
}

func replaceLinks(s string) string {
	for {
		i := strings.Index(s, "[")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "](")
		if j < 0 {
			break
		}
		k := strings.Index(s[i+j+2:], ")")
		if k < 0 {
			break
		}
		text := s[i+1 : i+j]
		href := s[i+j+2 : i+j+2+k]
		anchor := fmt.Sprintf(`<a href="%s">%s</a>`, template.HTMLEscapeString(href), text)
		s = s[:i] + anchor + s[i+j+2+k+1:]
	}
	return s
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"service":    serviceName,
		"env":        envName,
		"version":    versionStr,
		"uptime_sec": int64(time.Since(startedAt).Seconds()),
	})
}

func listDocsTree() ([]navEntry, error) {
	var out []navEntry
	err := fs.WalkDir(docsFS, "docs", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".md") {
			return nil
		}
		rel := strings.TrimPrefix(p, "docs/")
		out = append(out, navEntry{
			Name: rel,
			Path: "/" + strings.TrimSuffix(rel, ".md") + ".html",
			IsMD: true,
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	tree, err := listDocsTree()
	if err != nil {
		http.Error(w, "failed to enumerate docs", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	_ = pageTpl.Execute(w, page{
		Title: "r1 documentation",
		Path:  "/",
		Body: template.HTML(`<p>Welcome to the r1 documentation site. Pick a doc on the left, or pull the latest binary from <a href="https://downloads.r1.run/">downloads.r1.run</a>.</p>` +
			`<p>r1 is a local-first agent harness — daemon, CLI, web chat UI, and Tauri desktop. The daemon runs on your machine; this site is the public-facing reference for what r1 does and how to operate it.</p>`),
		Children: tree,
	})
}

func handleDoc(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		handleIndex(w, r)
		return
	}
	if p == "favicon.ico" {
		http.NotFound(w, r)
		return
	}
	// Strip extension; resolve to embedded docs/<p>.md
	name := strings.TrimSuffix(strings.TrimSuffix(p, ".html"), ".md")
	candidate := path.Join("docs", name+".md")
	src, err := docsFS.ReadFile(candidate)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	body := renderMarkdown(src)
	tree, _ := listDocsTree()
	_ = pageTpl.Execute(w, page{
		Title:    titleFromMarkdown(src, name),
		Path:     "/" + p,
		Body:     body,
		Children: tree,
	})
}

func handleRaw(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/raw/")
	candidate := path.Join("docs", p)
	src, err := docsFS.ReadFile(candidate)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write(src)
}

func titleFromMarkdown(src []byte, fallback string) string {
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(line[2:])
		}
	}
	return fallback
}

func main() {
	port := getenv("PORT", "8080")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/livez", handleHealthz)
	mux.HandleFunc("/readyz", handleHealthz)
	mux.HandleFunc("/raw/", handleRaw)
	mux.HandleFunc("/", handleDoc)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("%s listening on :%s (env=%s version=%s)", serviceName, port, envName, versionStr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}
