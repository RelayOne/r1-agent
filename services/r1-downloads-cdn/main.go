// Package main implements the r1 binary download CDN.
//
// It exposes signed-URL-style read access to release artifacts in
// gs://relayone-488319-r1-releases/{prod,staging,dev}/<asset>. Cloud
// Run runs as a service account with `roles/storage.objectViewer` on
// that bucket; the handler streams the requested object back to the
// caller without re-buffering when possible.
//
// Endpoints:
//
//	GET /healthz       — liveness
//	GET /             — JSON index of available channels + assets
//	GET /<channel>/<asset>
//	GET /<channel>/<asset>/sha256
//
// Channel = prod | staging | dev. Asset = filename inside the channel.
//
// Security: the bucket is private; we proxy through Cloud Run so the
// service account is the only entity that needs `objectViewer`. CORS is
// open to support `curl -L https://downloads.r1.run/prod/r1-linux-amd64`.
//
// Env:
//
//	R1_BUCKET   default "relayone-488319-r1-releases"
//	R1_ENV      "prod" | "staging" | "dev"
//	R1_VERSION  short SHA
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

const serviceName = "r1-downloads-cdn"

var (
	startedAt  = time.Now()
	envName    = getenv("R1_ENV", "dev")
	versionStr = getenv("R1_VERSION", "dev")
	bucketName = getenv("R1_BUCKET", "relayone-488319-r1-releases")
	allowed    = map[string]bool{"prod": true, "staging": true, "dev": true}
)

func getenv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type server struct {
	client *storage.Client
}

func newServer(ctx context.Context) (*server, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage client: %w", err)
	}
	return &server{client: client}, nil
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"service":    serviceName,
		"env":        envName,
		"version":    versionStr,
		"bucket":     bucketName,
		"uptime_sec": int64(time.Since(startedAt).Seconds()),
	})
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	channels := map[string][]string{}
	for ch := range allowed {
		assets, err := s.listChannel(ctx, ch)
		if err != nil {
			log.Printf("listChannel %s: %v", ch, err)
			channels[ch] = []string{}
			continue
		}
		channels[ch] = assets
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service":  serviceName,
		"bucket":   bucketName,
		"channels": channels,
	})
}

func (s *server) listChannel(ctx context.Context, channel string) ([]string, error) {
	if !allowed[channel] {
		return nil, fmt.Errorf("invalid channel: %s", channel)
	}
	out := []string{}
	it := s.client.Bucket(bucketName).Objects(ctx, &storage.Query{Prefix: channel + "/"})
	for {
		obj, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return out, err
		}
		out = append(out, strings.TrimPrefix(obj.Name, channel+"/"))
	}
	return out, nil
}

func (s *server) handleObject(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.Trim(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	channel := parts[0]
	asset := parts[1]
	wantSha := len(parts) == 3 && parts[2] == "sha256"

	if !allowed[channel] {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "error": "invalid channel; expected one of prod/staging/dev",
		})
		return
	}
	if asset == "" || strings.Contains(asset, "..") {
		http.Error(w, "invalid asset", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	objName := channel + "/" + asset
	obj := s.client.Bucket(bucketName).Object(objName)
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		log.Printf("attrs %q: %v", objName, err)
		http.NotFound(w, r)
		return
	}
	if wantSha {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"object": objName,
			"size":   attrs.Size,
			"md5":    fmt.Sprintf("%x", attrs.MD5),
			"crc32c": attrs.CRC32C,
		})
		return
	}

	rc, err := obj.NewReader(ctx)
	if err != nil {
		log.Printf("newReader %q: %v", objName, err)
		http.Error(w, "stream open failed", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", attrs.ContentType)
	if attrs.ContentType == "" {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", attrs.Size))
	w.Header().Set("X-Object-MD5", fmt.Sprintf("%x", attrs.MD5))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("copy %q: %v", objName, err)
	}
}

func (s *server) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		s.handleIndex(w, r)
		return
	}
	s.handleObject(w, r)
}

func main() {
	port := getenv("PORT", "8080")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv, err := newServer(ctx)
	if err != nil {
		log.Fatalf("new server: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", srv.handleHealthz)
	mux.HandleFunc("/livez", srv.handleHealthz)
	mux.HandleFunc("/readyz", srv.handleHealthz)
	mux.HandleFunc("/", srv.handle)

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("%s listening on :%s (env=%s version=%s bucket=%s)", serviceName, port, envName, versionStr, bucketName)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}
