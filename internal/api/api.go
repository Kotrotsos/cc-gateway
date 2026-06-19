// Package api serves the read side of the gateway: a small JSON REST API over
// the store plus a live SSE stream of new exchanges, and the embedded React UI.
package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"cc-gateway/internal/ingest"
	"cc-gateway/internal/store"
)

// Server wires the store and live broadcaster to HTTP handlers.
type Server struct {
	store  *store.Store
	bus    *ingest.Broadcaster
	static fs.FS
}

// New builds the API server. bus may be nil (no live stream) and static may be
// nil (no embedded UI).
func New(st *store.Store, bus *ingest.Broadcaster, static fs.FS) *Server {
	return &Server{store: st, bus: bus, static: static}
}

// Handler returns the root mux: /api/* endpoints plus the SPA static handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions", s.listSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.getSession)
	mux.HandleFunc("GET /api/requests/{id}", s.getRequest)
	mux.HandleFunc("GET /api/analytics", s.analytics)
	mux.HandleFunc("GET /api/analytics/tools/{name}", s.toolCalls)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("GET /api/models", s.models)
	mux.HandleFunc("GET /api/stream", s.stream)
	if s.static != nil {
		mux.Handle("GET /", s.spa())
	}
	return mux
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	q := store.SessionQuery{
		Model:     r.URL.Query().Get("model"),
		Tool:      r.URL.Query().Get("tool"),
		HasErrors: r.URL.Query().Get("errors") == "1",
		Q:         r.URL.Query().Get("q"),
		Limit:     atoiDefault(r.URL.Query().Get("limit"), 100),
		Offset:    atoiDefault(r.URL.Query().Get("offset"), 0),
	}
	res, err := s.store.ListSessions(q)
	writeJSON(w, res, err)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	res, err := s.store.GetSession(id)
	writeJSON(w, res, err)
}

func (s *Server) getRequest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	res, err := s.store.GetRequest(id, r.URL.Query().Get("raw") == "1")
	writeJSON(w, res, err)
}

func (s *Server) analytics(w http.ResponseWriter, r *http.Request) {
	q := store.AnalyticsQuery{
		Since:     atoi64(r.URL.Query().Get("since")),
		Until:     atoi64(r.URL.Query().Get("until")),
		SessionID: atoi64(r.URL.Query().Get("session")),
		BucketMs:  atoi64(r.URL.Query().Get("bucket")),
	}
	res, err := s.store.Analytics(q)
	writeJSON(w, res, err)
}

func (s *Server) toolCalls(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	res, err := s.store.ToolCalls(name, atoiDefault(r.URL.Query().Get("limit"), 200))
	writeJSON(w, res, err)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, []store.SearchHit{}, nil)
		return
	}
	res, err := s.store.Search(q, atoiDefault(r.URL.Query().Get("limit"), 50))
	writeJSON(w, res, err)
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	res, err := s.store.Models()
	writeJSON(w, res, err)
}

// spa serves the embedded static assets, falling back to index.html for client
// routes (anything that isn't a real file and isn't an /api path).
func (s *Server) spa() http.Handler {
	fileServer := http.FileServer(http.FS(s.static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(s.static, p); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
