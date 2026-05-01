package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/r1env"
)

type skillPackRegistryServer struct {
	sourceRoot string
	token      string
}

type registryPackSummary struct {
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Description     string   `json:"description,omitempty"`
	Dependencies    []string `json:"dependencies,omitempty"`
	DeclaredSkills  int      `json:"declared_skills"`
	ManifestCount   int      `json:"manifest_count"`
	Signed          bool     `json:"signed"`
	SignatureKeyID  string   `json:"signature_key_id,omitempty"`
	DownloadURLPath string   `json:"download_url_path"`
}

type registryPackDetail struct {
	registryPackSummary
	ManifestNames []string `json:"manifest_names"`
	SourcePath    string   `json:"source_path"`
}

func runSkillsPackServeCmd(args []string) {
	addr, sourceRoot, token := parseSkillPackServeArgs(args)
	server := newSkillPackRegistryServer(sourceRoot, token)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "skills pack serve listening on %s (source_root=%s auth=%t)\n", addr, sourceRoot, token != "")
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "skills pack serve: %v\n", err)
		os.Exit(1)
	}
}

func parseSkillPackServeArgs(args []string) (string, string, string) {
	fs := flag.NewFlagSet("skills pack serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:3949", "listen address")
	sourceRoot := fs.String("source-root", "", "root that contains .r1/.stoke skill pack libraries (defaults to HOME)")
	token := fs.String("token", "", "optional bearer token required for every request")
	fs.Parse(args)
	resolvedRoot, err := resolveSkillPackPublishRoot(*sourceRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills pack serve: %v\n", err)
		os.Exit(2)
	}
	resolvedToken := strings.TrimSpace(*token)
	if resolvedToken == "" {
		resolvedToken = strings.TrimSpace(r1env.Get("R1_SKILL_PACK_REGISTRY_TOKEN", "STOKE_SKILL_PACK_REGISTRY_TOKEN"))
	}
	return *addr, resolvedRoot, resolvedToken
}

func newSkillPackRegistryServer(sourceRoot, token string) *skillPackRegistryServer {
	return &skillPackRegistryServer{
		sourceRoot: sourceRoot,
		token:      strings.TrimSpace(token),
	}
}

func (s *skillPackRegistryServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/packs", s.handleList)
	mux.HandleFunc("/v1/packs/", s.handlePack)
	return mux
}

func (s *skillPackRegistryServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	if !s.authorize(w, r) {
		return
	}
	writeRegistryJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"source_root": s.sourceRoot,
	})
}

func (s *skillPackRegistryServer) handleList(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/packs" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	if !s.authorize(w, r) {
		return
	}
	packs, err := s.listPacks()
	if err != nil {
		writeRegistryJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeRegistryJSON(w, http.StatusOK, map[string]any{
		"source_root": s.sourceRoot,
		"pack_count":  len(packs),
		"packs":       packs,
	})
}

func (s *skillPackRegistryServer) handlePack(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/v1/packs/")
	if trimmed == "" || trimmed == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(trimmed, "/")
	packName := strings.TrimSpace(parts[0])
	if packName == "" {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(parts) == 1:
		s.handlePackDetail(w, r, packName)
	case len(parts) == 2 && parts[1] == "archive.tar.gz":
		s.handlePackArchive(w, r, packName)
	default:
		http.NotFound(w, r)
	}
}

func (s *skillPackRegistryServer) handlePackDetail(w http.ResponseWriter, _ *http.Request, packName string) {
	detail, err := s.loadPackDetail(packName)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeRegistryJSONError(w, status, err.Error())
		return
	}
	writeRegistryJSON(w, http.StatusOK, detail)
}

func (s *skillPackRegistryServer) handlePackArchive(w http.ResponseWriter, _ *http.Request, packName string) {
	packPath, err := s.resolvePackPath(packName)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeRegistryJSONError(w, status, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", packName+".tar.gz"))
	if err := writePackArchive(w, packPath, packName); err != nil {
		writeRegistryJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
}

func (s *skillPackRegistryServer) authorize(w http.ResponseWriter, r *http.Request) bool {
	if s.token == "" {
		return true
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header != "Bearer "+s.token {
		w.Header().Set("WWW-Authenticate", `Bearer realm="skills-pack-registry"`)
		writeRegistryJSONError(w, http.StatusUnauthorized, "authorization required")
		return false
	}
	return true
}

func (s *skillPackRegistryServer) listPacks() ([]registryPackSummary, error) {
	packPaths, err := registryPackPaths(s.sourceRoot)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(packPaths))
	for name := range packPaths {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]registryPackSummary, 0, len(names))
	for _, name := range names {
		detail, err := buildRegistryPackDetail(packPaths[name])
		if err != nil {
			return nil, err
		}
		out = append(out, detail.registryPackSummary)
	}
	return out, nil
}

func (s *skillPackRegistryServer) loadPackDetail(packName string) (*registryPackDetail, error) {
	packPath, err := s.resolvePackPath(packName)
	if err != nil {
		return nil, err
	}
	return buildRegistryPackDetail(packPath)
}

func (s *skillPackRegistryServer) resolvePackPath(packName string) (string, error) {
	packPaths, err := registryPackPaths(s.sourceRoot)
	if err != nil {
		return "", err
	}
	packPath, ok := packPaths[packName]
	if !ok {
		return "", &os.PathError{
			Op:   "open",
			Path: filepath.Join(s.sourceRoot, r1dir.Canonical, "skills", "packs", packName),
			Err:  fs.ErrNotExist,
		}
	}
	return packPath, nil
}

func registryPackPaths(sourceRoot string) (map[string]string, error) {
	if strings.TrimSpace(sourceRoot) == "" {
		return nil, fmt.Errorf("source root required")
	}
	roots := []string{
		filepath.Join(sourceRoot, r1dir.Canonical, "skills", "packs"),
		filepath.Join(sourceRoot, r1dir.Legacy, "skills", "packs"),
	}
	packs := make(map[string]string)
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read pack registry %q: %w", root, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, exists := packs[entry.Name()]; exists {
				continue
			}
			packs[entry.Name()] = filepath.Join(root, entry.Name())
		}
	}
	return packs, nil
}

func buildRegistryPackDetail(packPath string) (*registryPackDetail, error) {
	pack, signature, err := loadSkillPackWithSignature(packPath)
	if err != nil {
		return nil, err
	}
	manifestNames := make([]string, 0, len(pack.Manifests))
	for _, manifest := range pack.Manifests {
		manifestNames = append(manifestNames, manifest.Name)
	}
	sort.Strings(manifestNames)
	return &registryPackDetail{
		registryPackSummary: registryPackSummary{
			Name:            pack.Meta.Name,
			Version:         pack.Meta.Version,
			Description:     pack.Meta.Description,
			Dependencies:    append([]string(nil), pack.Meta.Dependencies...),
			DeclaredSkills:  pack.Meta.SkillCount,
			ManifestCount:   len(pack.Manifests),
			Signed:          signature != nil,
			SignatureKeyID:  signatureKeyID(signature),
			DownloadURLPath: fmt.Sprintf("/v1/packs/%s/archive.tar.gz", pack.Meta.Name),
		},
		ManifestNames: manifestNames,
		SourcePath:    packPath,
	}, nil
}

func writePackArchive(dst io.Writer, packPath, packName string) error {
	gz := gzip.NewWriter(dst)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.WalkDir(packPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(packPath, path)
		if err != nil {
			return fmt.Errorf("relative archive path for %q: %w", path, err)
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(filepath.Join(packName, rel))
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %q: %w", path, err)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive pack %q contains symlink %q", packPath, path)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("tar header %q: %w", path, err)
		}
		header.Name = rel
		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("tar write header %q: %w", rel, err)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %q: %w", path, err)
		}
		if _, err := tw.Write(payload); err != nil {
			return fmt.Errorf("tar write %q: %w", rel, err)
		}
		return nil
	})
}

func writeRegistryJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeRegistryJSONError(w http.ResponseWriter, status int, message string) {
	writeRegistryJSON(w, status, map[string]string{"error": message})
}

func writeMethodNotAllowed(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	writeRegistryJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
}
