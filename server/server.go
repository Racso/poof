package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/racso/poof/config"
	"github.com/racso/poof/store"
	"github.com/racso/poof/version"
)

// RepoManager abstracts GitHub repository operations (secrets + workflow files)
// so that handlers can be tested without hitting the GitHub API.
type RepoManager interface {
	SetRepoCI(owner, repo, projectName, poofURL, poofToken, branch, image, folder, static string) error
	RemoveRepoCI(owner, repo, projectName string, deleteSecrets bool) error
	RefreshProjectCI(owner, repo, projectName string, ci bool, poofURL, repoToken, branch, image, folder, static string, deleteSecrets bool) error
}

type Server struct {
	cfg       *config.ServerConfig
	store     *store.Store
	logPath   string
	ghFactory func(token string) RepoManager
}

func New(cfg *config.ServerConfig, st *store.Store, ghFactory func(token string) RepoManager) *Server {
	return &Server{
		cfg:       cfg,
		store:     st,
		logPath:   filepath.Join(cfg.DataDir, "server.log"),
		ghFactory: ghFactory,
	}
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
	mux.HandleFunc("POST /projects/{name}/clone", s.auth(s.cloneProject))

	// Deploy & rollback — accept both global token AND per-project token
	// so the GH Action can call /projects/:name/deploy with its per-project token.
	mux.HandleFunc("POST /projects/{name}/deploy", s.authFlex(s.deployProject))
	mux.HandleFunc("POST /projects/{name}/deploy/static", s.authFlex(s.deployStaticProject))
	mux.HandleFunc("POST /projects/{name}/rollback", s.auth(s.rollbackProject))
	mux.HandleFunc("POST /projects/{name}/refresh", s.auth(s.refreshProject))

	// Logs
	mux.HandleFunc("GET /projects/{name}/logs", s.auth(s.getLogs))

	// Env vars
	mux.HandleFunc("GET /projects/{name}/env", s.auth(s.getEnv))
	mux.HandleFunc("PUT /projects/{name}/env", s.auth(s.setEnv))
	mux.HandleFunc("DELETE /projects/{name}/env/{key}", s.auth(s.unsetEnv))
	mux.HandleFunc("POST /projects/{name}/env/copy-to/{target}", s.auth(s.copyEnv))

	// Volumes
	mux.HandleFunc("GET /projects/{name}/volumes", s.auth(s.listVolumes))
	mux.HandleFunc("POST /projects/{name}/volumes", s.auth(s.addVolume))
	mux.HandleFunc("GET /projects/{name}/volumes/{id}", s.auth(s.getVolume))
	mux.HandleFunc("DELETE /projects/{name}/volumes/{id}", s.auth(s.removeVolume))

	// Redirects
	mux.HandleFunc("GET /redirects", s.auth(s.listRedirects))
	mux.HandleFunc("POST /redirects", s.auth(s.createRedirect))
	mux.HandleFunc("DELETE /redirects/{id}", s.auth(s.deleteRedirect))

	// Server settings
	mux.HandleFunc("GET /config", s.auth(s.getConfig))
	mux.HandleFunc("PATCH /config/{key}", s.auth(s.setConfig))

	// Server logs, version, and self-update
	mux.HandleFunc("GET /logs/server", s.auth(s.getServerLogs))
	mux.HandleFunc("GET /version", s.auth(s.getVersion))
	mux.HandleFunc("POST /update", s.auth(s.updateServer))

	return mux
}

// requestLogger wraps a handler and logs method, path, status, and duration.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), rw.code, time.Since(start).Round(time.Millisecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.code = code
	sw.ResponseWriter.WriteHeader(code)
}

func (s *Server) getServerLogs(w http.ResponseWriter, r *http.Request) {
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			lines = n
		}
	}
	tail, err := tailFile(s.logPath, lines)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(tail))
}

// tailFile returns the last n lines of the file at path.
func tailFile(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	result := ""
	for _, l := range lines {
		result += l + "\n"
	}
	return result, nil
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{
		"number":      version.Number,
		"commit":      version.Commit,
		"commit_time": version.CommitTime,
	})
}

// ServeHTTP implements http.Handler, allowing the server to be used with
// httptest.NewRecorder in tests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler().ServeHTTP(w, r)
}

func (s *Server) Run() error {
	f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", s.logPath, err)
	}
	defer f.Close()
	log.SetOutput(io.MultiWriter(os.Stdout, f))
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC)

	addr := fmt.Sprintf(":%d", s.cfg.APIPort)
	log.Printf("poof server starting — commit=%s committed=%s addr=%s", version.Commit, version.CommitTime, addr)

	if err := s.syncCaddy(); err != nil {
		log.Printf("warning: initial caddy sync failed: %v", err)
	}

	return http.ListenAndServe(addr, s.requestLogger(s.handler()))
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

// authFlex middleware: accepts global token OR the repo-level deploy token.
// Used for /deploy so the GH Action can call it without the global token.
func (s *Server) authFlex(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.validGlobalToken(r) {
			next(w, r)
			return
		}
		// Try repo-level deploy token.
		name := r.PathValue("name")
		p, err := s.store.GetProject(name)
		if err != nil || p == nil {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		repoToken, _ := s.store.GetRepoToken(p.Repo)
		token := bearerToken(r)
		if token == "" || repoToken == "" || token != repoToken {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) validGlobalToken(r *http.Request) bool {
	// Resolve the expected token: toml takes precedence, then DB, then bootstrap.
	token := bearerToken(r)
	return token != "" && token == s.cfg.Token
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
