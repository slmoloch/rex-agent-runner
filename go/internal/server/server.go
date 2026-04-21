// Package server exposes rex's HTTP surface: the embedded dashboard UI,
// event + session JSON APIs, and the /job endpoint used by the dispatch
// CLI and scheduled callbacks.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/slmoloch/rex-agent-runner/internal/assets"
	"github.com/slmoloch/rex-agent-runner/internal/session"
	"github.com/slmoloch/rex-agent-runner/internal/timeline"
)

// JobFunc processes one /job request and returns the response text.
type JobFunc func(ctx context.Context, prompt, jobName, sessionTarget, callerSession string) (string, error)

type Server struct {
	timeline *timeline.Store
	session  *session.Store
	job      JobFunc
}

// New wires the dashboard and JSON APIs. The timeline store is the read path
// for /api/events (indexed SQLite queries).
func New(tl *timeline.Store, se *session.Store, job JobFunc) *Server {
	return &Server{timeline: tl, session: se, job: job}
}

// Routes returns an http.Handler mounting every rex endpoint.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Dashboard (index) + static assets. `web/` is embedded into the binary.
	webFS, _ := fs.Sub(assets.Web, "web")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			data, err := fs.ReadFile(webFS, "timeline.html")
			if err != nil {
				http.Error(w, "timeline not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	})
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(webFS))))

	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/job", s.handleJob)
	return mux
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	rows, err := s.timeline.Query(7, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	tracked := s.session.Tracked()
	mainID := s.session.GetMainID()
	type row struct {
		SessionID    string `json:"session_id"`
		Name         string `json:"name,omitempty"`
		LastActivity string `json:"last_activity,omitempty"`
		IsMain       bool   `json:"is_main"`
		IsRunning    bool   `json:"is_running"`
	}
	rows := make([]row, 0, len(tracked))
	for id, t := range tracked {
		rows = append(rows, row{
			SessionID:    id,
			Name:         t.Name,
			LastActivity: t.LastActivity,
			IsMain:       id == mainID,
			IsRunning:    s.session.IsRunning(id),
		})
	}
	// Sort newest-first by last_activity.
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].LastActivity > rows[i].LastActivity {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	writeJSON(w, rows)
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Prompt        string `json:"prompt"`
		JobName       string `json:"job_name"`
		Session       string `json:"session"`
		CallerSession string `json:"caller_session,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.Prompt == "" {
		http.Error(w, "prompt required", http.StatusBadRequest)
		return
	}
	if req.Session == "" {
		req.Session = session.Main
	}
	if req.JobName == "" {
		req.JobName = "unknown"
	}
	slog.Info("job received", "job", req.JobName, "session", req.Session)
	resp, err := s.job(r.Context(), req.Prompt, req.JobName, req.Session, req.CallerSession)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	max := 1000
	if len(resp) < max {
		max = len(resp)
	}
	writeJSON(w, map[string]any{
		"status":   "ok",
		"response": resp[:max],
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// Listen binds on 127.0.0.1:port. Returns an http.Server the caller can close.
func Listen(port int, h http.Handler) (*http.Server, error) {
	srv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: h,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "err", err)
		}
	}()
	slog.Info("dashboard listening", "url", fmt.Sprintf("http://127.0.0.1:%d/", port))
	return srv, nil
}
