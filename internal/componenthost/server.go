package componenthost

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"alice/internal/core"
	"alice/internal/dynamic"
	"alice/pkg/component"
)

type Server struct {
	registry *core.Registry
	manager  *dynamic.Manager
	token    string
}

func New(sourceRoot, dataDir, token string) (*Server, error) {
	registry := core.NewRegistry()
	manager := dynamic.NewManager(registry, sourceRoot, dataDir+"/bin", dataDir+"/installed.json")
	if err := manager.Load(); err != nil {
		return nil, err
	}
	return &Server{registry: registry, manager: manager, token: token}, nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UnixMilli()})
	})
	mux.HandleFunc("GET /api/components", s.components)
	mux.HandleFunc("POST /api/build", s.build)
	mux.HandleFunc("POST /api/build-source", s.buildSource)
	mux.HandleFunc("POST /api/invoke", s.invoke)
	return s.auth(mux)
}
func (s *Server) buildSource(w http.ResponseWriter, r *http.Request) {
	var request dynamic.GeneratedSource
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request); err != nil {
		writeErr(w, err)
		return
	}
	manifest, err := s.manager.BuildSource(r.Context(), request)
	if err != nil {
		writeErr(w, err)
		return
	}
	write(w, http.StatusOK, manifest)
}
func (s *Server) components(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, map[string]any{"toolchain": s.manager.Toolchain(), "components": s.manager.Manifests()})
}
func (s *Server) build(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SourceDir string `json:"source_dir"`
		Activate  bool   `json:"activate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeErr(w, err)
		return
	}
	manifest, err := s.manager.Build(r.Context(), request.SourceDir, request.Activate)
	if err != nil {
		writeErr(w, err)
		return
	}
	write(w, http.StatusOK, manifest)
}
func (s *Server) invoke(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ComponentID string             `json:"component_id"`
		Version     string             `json:"version"`
		Input       component.Envelope `json:"input"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&request); err != nil {
		writeErr(w, err)
		return
	}
	c, err := s.registry.Resolve(request.ComponentID, request.Version)
	if err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	output, err := c.Execute(ctx, request.Input)
	if err != nil {
		writeErr(w, err)
		return
	}
	write(w, http.StatusOK, output)
}
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.URL.Path != "/health" && strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != s.token {
			write(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeErr(w http.ResponseWriter, err error) {
	write(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}
