package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/racso/poof/config"
	"github.com/racso/poof/store"
	"github.com/racso/poof/version"
)

type Server struct {
	cfg   *config.ServerConfig
	store *store.Store
}

func New(cfg *config.ServerConfig, st *store.Store) *Server {
	return &Server{cfg: cfg, store: st}
}

// handler builds and returns the HTTP mux. Separated from Run so tests can
// call ServeHTTP directly without binding to a port.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()

	// Project management (requires global API token)
	mux.HandleFunc("GET /projects", s.auth(s.listProjects))
	mux.HandleFunc("POST /projects", s.auth(s.createProject))
	mux.HandleFunc("GET /projects/{name}", s.auth(s.getProject))
	mux.HandleFunc("PATCH /projects/{name}", s.auth(s.updateProject))
	mux.HandleFunc("DELETE /projects/{name}", s.auth(s.deleteProject))

	// Deploy & rollback — accept both global token AND per-project token
	// so the GH Action can call /projects/:name/deploy with its per-project token.
	mux.HandleFunc("POST /projects/{name}/deploy", s.authFlex(s.deployProject))
	mux.HandleFunc("POST /projects/{name}/rollback", s.auth(s.rollbackProject))

	// Logs
	mux.HandleFunc("GET /projects/{name}/logs", s.auth(s.getLogs))

	// Env vars
	mux.HandleFunc("GET /projects/{name}/env", s.auth(s.getEnv))
	mux.HandleFunc("PUT /projects/{name}/env", s.auth(s.setEnv))
	mux.HandleFunc("DELETE /projects/{name}/env/{key}", s.auth(s.unsetEnv))

	// Version
	mux.HandleFunc("GET /version", s.auth(s.getVersion))

	return mux
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{
		"commit":     version.Commit,
		"build_time": version.BuildTime,
	})
}

// ServeHTTP implements http.Handler, allowing the server to be used with
// httptest.NewRecorder in tests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler().ServeHTTP(w, r)
}

func (s *Server) Run() error {
	addr := fmt.Sprintf(":%d", s.cfg.APIPort)
	log.Printf("poof server listening on %s", addr)
	return http.ListenAndServe(addr, s.handler())
}

// auth middleware: requires the global API token.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validGlobalToken(r) {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// authFlex middleware: accepts global token OR the project's per-project token.
// Used for /deploy so the GH Action can call it without the global token.
func (s *Server) authFlex(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.validGlobalToken(r) {
			next(w, r)
			return
		}
		// Try per-project token
		name := r.PathValue("name")
		p, err := s.store.GetProject(name)
		if err != nil || p == nil {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token := bearerToken(r)
		if token == "" || token != p.Token {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) validGlobalToken(r *http.Request) bool {
	token := bearerToken(r)
	return token != "" && token == s.cfg.Auth.Token
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && h[:7] == "Bearer " {
		return h[7:]
	}
	return ""
}

func jsonOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
