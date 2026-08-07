package httpapi

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"alice/internal/app"
	"alice/internal/core"
	"alice/internal/models"
	"alice/internal/tasks"
	"alice/pkg/component"
)

//go:embed index.html
var indexHTML []byte

type Server struct{ app *app.App }

func New(a *app.App) *Server { return &Server{app: a} }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/snapshot", s.snapshot)
	mux.HandleFunc("GET /api/facts/search", s.searchFacts)
	mux.HandleFunc("GET /api/settings/model", s.getModelSettings)
	mux.HandleFunc("PUT /api/settings/model", s.putModelSettings)
	mux.HandleFunc("POST /api/settings/model/test", s.testModelSettings)
	mux.HandleFunc("GET /api/messages", s.messages)
	mux.HandleFunc("POST /api/chat", s.chat)
	mux.HandleFunc("POST /api/core/chat", s.coreChat)
	mux.HandleFunc("POST /api/blueprints", s.publishBlueprint)
	mux.HandleFunc("POST /api/blueprints/{id}/activate", s.activateBlueprint)
	mux.HandleFunc("POST /api/components/build", s.buildComponent)
	mux.HandleFunc("POST /api/components/{id}/activate", s.activateComponent)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("POST /api/tasks/{id}/trigger", s.triggerTask)
	mux.HandleFunc("POST /api/executions/{id}/patch", s.patchExecution)
	mux.HandleFunc("POST /api/executions/{id}/promote", s.promoteExecution)
	mux.HandleFunc("POST /api/executions/{id}/cancel", s.cancelExecution)
	return requestLog(mux)
}

func (s *Server) publishBlueprint(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Blueprint core.Blueprint `json:"blueprint"`
		Activate  bool           `json:"activate"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Blueprint.CreatedAt == 0 {
		request.Blueprint.CreatedAt = time.Now().UnixMilli()
	}
	if err := s.app.Blueprints.Publish(request.Blueprint, request.Activate); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, request.Blueprint)
}

func (s *Server) activateBlueprint(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Version int `json:"version"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if err := s.app.Blueprints.Activate(r.PathValue("id"), request.Version); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "activated"})
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UnixMilli()})
}

func (s *Server) snapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.app.Snapshot())
}

func (s *Server) searchFacts(w http.ResponseWriter, r *http.Request) {
	facts, err := s.app.Memory.Search(r.Context(), r.URL.Query().Get("q"), 20)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, facts)
}

func (s *Server) getModelSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.app.Models.PublicConfig())
}

func (s *Server) putModelSettings(w http.ResponseWriter, r *http.Request) {
	var request models.Config
	if !decodeJSON(w, r, &request) {
		return
	}
	if err := s.app.Models.Save(r.Context(), request); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.app.Models.PublicConfig())
}

func (s *Server) testModelSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.app.Models.Test(ctx); err != nil {
		writeError(w, err)
		return
	}
	if s.app.Models.EmbeddingConfigured() {
		if err := s.app.Vectors.Check(ctx); err != nil {
			writeError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.DB.RecentMessages(r.Context(), 100)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Text        string `json:"text"`
		Source      string `json:"source"`
		ReplyHandle string `json:"reply_handle"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Source == "" {
		request.Source = "web"
	}
	conversation, execution, err := s.app.Chat(r.Context(), request.Text, request.Source, request.ReplyHandle)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": conversation.Reply, "facts_created": conversation.CreatedFacts, "facts_recalled": conversation.RecalledFacts, "execution_id": execution.ID})
}

func (s *Server) coreChat(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	reply, execution, err := s.app.CoreChat(r.Context(), request.Text)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": reply, "execution_id": execution.ID})
}

func (s *Server) buildComponent(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SourceDir string `json:"source_dir"`
		Activate  bool   `json:"activate"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	manifest, execution, err := s.app.BuildComponent(r.Context(), request.SourceDir, request.Activate)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"manifest": manifest, "execution_id": execution.ID})
}

func (s *Server) activateComponent(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if err := s.app.Registry.Activate(r.PathValue("id"), request.Version); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "activated"})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Label            string          `json:"label"`
		BlueprintID      string          `json:"blueprint_id"`
		BlueprintVersion int             `json:"blueprint_version"`
		Trigger          tasks.Trigger   `json:"trigger"`
		Input            json.RawMessage `json:"input"`
		RememberResult   bool            `json:"remember_result"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.BlueprintID == "" {
		request.BlueprintID = "alice.chat.default"
	}
	input := request.Input
	if len(input) == 0 {
		input, _ = json.Marshal(map[string]string{"text": request.Label, "source": "task"})
	}
	task, err := s.app.Tasks.Create(tasks.Task{
		Label: request.Label, BlueprintID: request.BlueprintID, BlueprintVersion: request.BlueprintVersion,
		Trigger: request.Trigger, Input: component.Envelope{Source: "task", Schema: "alice.message.v1", Payload: input}, RememberResult: request.RememberResult,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) triggerTask(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Tasks.Trigger(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "running"})
}

func (s *Server) patchExecution(w http.ResponseWriter, r *http.Request) {
	var patch core.RuntimePatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	if err := s.app.Engine.ApplyPatch(r.PathValue("id"), patch); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
}

func (s *Server) promoteExecution(w http.ResponseWriter, r *http.Request) {
	var request struct {
		BlueprintID string `json:"blueprint_id"`
		Version     int    `json:"version"`
		Name        string `json:"name"`
		Activate    bool   `json:"activate"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	candidate, err := s.app.Engine.BlueprintCandidate(r.PathValue("id"), request.BlueprintID, request.Name, request.Version)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.app.Blueprints.Publish(candidate, request.Activate); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, candidate)
}

func (s *Server) cancelExecution(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Engine.Cancel(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, context.Canceled) {
		status = http.StatusRequestTimeout
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "..") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
