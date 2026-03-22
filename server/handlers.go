package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/racso/poof/docker"
	gh "github.com/racso/poof/github"
	"github.com/racso/poof/store"
)

// --- Project CRUD ---

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type projectStatus struct {
		store.Project
		Running bool `json:"running"`
	}
	var result []projectStatus
	for _, p := range projects {
		result = append(result, projectStatus{p, docker.IsRunning(p.Name)})
	}
	jsonOK(w, result)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	last, _ := s.store.LastDeployment(name)

	jsonOK(w, map[string]interface{}{
		"project":    p,
		"running":    docker.IsRunning(name),
		"deployment": last,
	})
}

type createProjectRequest struct {
	Name    string `json:"name"`
	Domain  string `json:"domain"`
	Image   string `json:"image"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Port    int    `json:"port"`
	Subpath string `json:"subpath"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Apply defaults
	if req.Domain == "" {
		req.Domain = req.Name + "." + s.cfg.Domain
	}
	if req.Image == "" {
		req.Image = fmt.Sprintf("ghcr.io/%s/%s", strings.ToLower(s.cfg.GitHub.User), req.Name)
	}
	if req.Repo == "" {
		req.Repo = fmt.Sprintf("%s/%s", s.cfg.GitHub.User, req.Name)
	}
	if req.Branch == "" {
		req.Branch = "main"
	}
	if req.Port == 0 {
		req.Port = 8080
	}

	// Apply subpath default and validate
	if req.Subpath == "" {
		req.Subpath = s.cfg.SubpathDefault
	}
	if req.Subpath == "" {
		req.Subpath = "disabled"
	}
	if !validSubpath(req.Subpath) {
		jsonError(w, "subpath must be disabled, redirect, or proxy", http.StatusBadRequest)
		return
	}

	// Validate required fields after defaults
	if req.Name == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}

	// Check duplicate
	existing, _ := s.store.GetProject(req.Name)
	if existing != nil {
		jsonError(w, "project already exists", http.StatusConflict)
		return
	}

	// Generate per-project deploy token
	token, err := generateToken()
	if err != nil {
		jsonError(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	p := store.Project{
		Name:    req.Name,
		Domain:  req.Domain,
		Image:   req.Image,
		Repo:    req.Repo,
		Branch:  req.Branch,
		Port:    req.Port,
		Token:   token,
		Subpath: req.Subpath,
	}

	if err := s.store.CreateProject(p); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Set up GitHub repo (secrets + workflow) if a PAT is configured.
	if s.cfg.GitHub.Token != "" {
		client := gh.NewClient(s.cfg.GitHub.Token)
		owner, repoName, found := strings.Cut(req.Repo, "/")
		if !found {
			owner = s.cfg.GitHub.User
			repoName = req.Repo
		}
		if err := client.SetupRepo(owner, repoName, s.cfg.PublicURL, token, req.Branch); err != nil {
			log.Printf("warning: GitHub setup for %s failed: %v", req.Name, err)
			// Don't fail — project is registered, user can retry manually.
		}
	}

	log.Printf("project created: %s (repo=%s branch=%s image=%s)", p.Name, p.Repo, p.Branch, p.Image)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, p)
}

type updateProjectRequest struct {
	Domain  string `json:"domain"`
	Image   string `json:"image"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Port    int    `json:"port"`
	Subpath string `json:"subpath"`
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	var req updateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	repoChanged := req.Repo != "" && req.Repo != p.Repo
	branchChanged := req.Branch != "" && req.Branch != p.Branch

	if req.Domain != "" {
		p.Domain = req.Domain
	}
	if req.Image != "" {
		p.Image = req.Image
	}
	if req.Repo != "" {
		p.Repo = req.Repo
	}
	if req.Branch != "" {
		p.Branch = req.Branch
	}
	if req.Port != 0 {
		p.Port = req.Port
	}
	if req.Subpath != "" {
		if !validSubpath(req.Subpath) {
			jsonError(w, "subpath must be disabled, redirect, or proxy", http.StatusBadRequest)
			return
		}
		p.Subpath = req.Subpath
	}

	if err := s.store.UpdateProject(*p); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("project updated: %s", name)
	if s.cfg.GitHub.Token != "" && (repoChanged || branchChanged) {
		client := gh.NewClient(s.cfg.GitHub.Token)
		owner, repoName, found := strings.Cut(p.Repo, "/")
		if !found {
			owner = s.cfg.GitHub.User
			repoName = p.Repo
		}
		if err := client.SetupRepo(owner, repoName, s.cfg.PublicURL, p.Token, p.Branch); err != nil {
			log.Printf("warning: GitHub update for %s failed: %v", name, err)
		}
	}

	jsonOK(w, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	// Stop container
	if err := docker.Stop(name); err != nil {
		log.Printf("warning: stopping container for %s: %v", name, err)
	}

	// Clean up GitHub if PAT is configured
	if s.cfg.GitHub.Token != "" {
		client := gh.NewClient(s.cfg.GitHub.Token)
		owner, repoName, found := strings.Cut(p.Repo, "/")
		if !found {
			owner = s.cfg.GitHub.User
			repoName = p.Repo
		}
		if err := client.RemoveRepo(owner, repoName); err != nil {
			log.Printf("warning: GitHub cleanup for %s: %v", name, err)
		}
	}

	if err := s.store.DeleteProject(name); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("project deleted: %s", name)
	jsonOK(w, map[string]string{"status": "deleted"})
}

// --- Deploy & Rollback ---

type deployRequest struct {
	Image string `json:"image"`
}

func (s *Server) deployProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil || p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	var req deployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Image == "" {
		// No body — redeploy with latest recorded image.
		last, _ := s.store.LastDeployment(name)
		if last != nil {
			req.Image = last.Image
		} else {
			req.Image = p.Image
		}
	}

	s.runDeploy(w, p, req.Image)
}

func (s *Server) rollbackProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil || p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	prev, err := s.store.PreviousDeployment(name)
	if err != nil || prev == nil {
		jsonError(w, "no previous deployment to roll back to", http.StatusBadRequest)
		return
	}

	log.Printf("rollback triggered: %s → %s", name, prev.Image)
	s.runDeploy(w, p, prev.Image)
}

func (s *Server) runDeploy(w http.ResponseWriter, p *store.Project, image string) {
	envVars, err := s.store.GetEnvVars(p.Name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("deploy started: %s → %s", p.Name, image)
	depID, _ := s.store.RecordDeployment(p.Name, image, "running")

	err = docker.Deploy(docker.DeployConfig{
		Name:          p.Name,
		Image:         image,
		Domain:        p.Domain,
		RootDomain:    s.cfg.Domain,
		Port:          p.Port,
		Subpath:       p.Subpath,
		EnvVars:       envVars,
		RegistryUser:  s.cfg.GitHub.User,
		RegistryToken: s.cfg.GitHub.Token,
	})

	status := "success"
	if err != nil {
		status = "failed"
		s.store.UpdateDeploymentStatus(depID, status)
		jsonError(w, fmt.Sprintf("deploy failed: %v", err), http.StatusInternalServerError)
		return
	}

	s.store.UpdateDeploymentStatus(depID, status)
	log.Printf("deployed %s → %s", p.Name, image)

	jsonOK(w, map[string]interface{}{
		"status": "deployed",
		"image":  image,
		"domain": p.Domain,
	})
}

// --- Logs ---

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			lines = n
		}
	}

	logs, err := docker.Logs(name, lines)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(logs))
}

// --- Env Vars ---

func (s *Server) getEnv(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	vars, err := s.store.GetEnvVars(name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return keys only — never expose values via API.
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	jsonOK(w, map[string]interface{}{"keys": keys})
}

func (s *Server) setEnv(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil || p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	var vars map[string]string
	if err := json.NewDecoder(r.Body).Decode(&vars); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	for k, v := range vars {
		if err := s.store.SetEnvVar(name, k, v); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	log.Printf("env updated: %s (%d key(s) set)", name, len(vars))
	jsonOK(w, map[string]string{"status": "updated"})
}

func (s *Server) unsetEnv(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	key := r.PathValue("key")
	if err := s.store.UnsetEnvVar(name, key); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("env unset: %s key=%s", name, key)
	jsonOK(w, map[string]string{"status": "removed"})
}

// --- Self-update ---

func (s *Server) updateSelf(w http.ResponseWriter, r *http.Request) {
	// Save current image ID so the user has a rollback reference if needed.
	if id := docker.CurrentImageID(); id != "" {
		rollbackPath := filepath.Join(s.cfg.DataDir, "rollback-image")
		if err := os.WriteFile(rollbackPath, []byte(id+"\n"), 0644); err != nil {
			log.Printf("self-update: could not write rollback-image: %v", err)
		}
	}

	log.Printf("self-update: pulling new image")
	if err := docker.PullSelf(); err != nil {
		log.Printf("self-update: pull failed: %v", err)
		jsonError(w, fmt.Sprintf("pull failed: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("self-update: running pre-flight check")
	if err := docker.PreflightSelf(); err != nil {
		log.Printf("self-update: pre-flight failed, update aborted: %v", err)
		jsonError(w, fmt.Sprintf("pre-flight check failed, update aborted: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("self-update: pre-flight passed, initiating restart")
	jsonOK(w, map[string]string{
		"status": "Update initiated — server is restarting with the new image. Run `poof version` in a few seconds to confirm.",
	})

	// Stop after the handler returns so the response is guaranteed to be sent first.
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := docker.StopSelf(); err != nil {
			log.Printf("self-update: stop failed: %v", err)
		}
	}()
}

// --- Helpers ---

func validSubpath(mode string) bool {
	switch mode {
	case "disabled", "redirect", "proxy":
		return true
	}
	return false
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
