package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/racso/poof/caddy"
	"github.com/racso/poof/defaults"
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
	Folder  string `json:"folder"`
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
		req.Image = fmt.Sprintf("ghcr.io/%s/%s", strings.ToLower(s.cfg.GitHub.User), strings.ToLower(req.Name))
	}
	if req.Repo == "" {
		req.Repo = fmt.Sprintf("%s/%s", s.cfg.GitHub.User, req.Name)
	}
	if req.Branch == "" {
		req.Branch = defaults.Branch
	}
	if req.Port == 0 {
		req.Port = defaults.Port
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
		Folder:  req.Folder,
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
		if err := client.SetupRepo(owner, repoName, req.Name, s.cfg.PublicURL, token, req.Branch, req.Image, req.Folder); err != nil {
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
	Folder  string `json:"folder"`
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
	folderChanged := req.Folder != p.Folder && (req.Folder != "" || p.Folder != "")

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
	// folder can be cleared by passing an explicit empty string via the flag;
	// only update if the field was present in the request body (handled by folderChanged check above).
	if req.Folder != "" || folderChanged {
		p.Folder = req.Folder
	}

	if err := s.store.UpdateProject(*p); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("project updated: %s", name)
	if s.cfg.GitHub.Token != "" && (repoChanged || branchChanged || folderChanged) {
		client := gh.NewClient(s.cfg.GitHub.Token)
		owner, repoName, found := strings.Cut(p.Repo, "/")
		if !found {
			owner = s.cfg.GitHub.User
			repoName = p.Repo
		}
		if err := client.SetupRepo(owner, repoName, p.Name, s.cfg.PublicURL, p.Token, p.Branch, p.Image, p.Folder); err != nil {
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

	if r.URL.Query().Get("data") == "delete" {
		dataDir := "/var/lib/poof/" + name
		if err := os.RemoveAll(dataDir); err != nil {
			log.Printf("warning: failed to purge host data for %s (%s): %v", name, dataDir, err)
		} else {
			log.Printf("project data purged: %s", dataDir)
		}
	}

	if err := s.syncCaddy(); err != nil {
		log.Printf("warning: caddy sync after delete failed: %v", err)
	}

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

	vols, err := s.store.ListVolumes(p.Name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mounts := make([]string, len(vols))
	for i, v := range vols {
		mounts[i] = v.HostPath + ":" + v.ContainerPath
	}

	log.Printf("deploy started: %s → %s", p.Name, image)
	depID, _ := s.store.RecordDeployment(p.Name, image, "running")

	err = docker.Deploy(docker.DeployConfig{
		Name:          p.Name,
		Image:         image,
		EnvVars:       envVars,
		Volumes:       mounts,
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

	if err := s.syncCaddy(); err != nil {
		log.Printf("warning: caddy sync after deploy failed: %v", err)
	}

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

// --- Redirects ---

func (s *Server) listRedirects(w http.ResponseWriter, r *http.Request) {
	redirects, err := s.store.ListRedirects()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if redirects == nil {
		redirects = []store.Redirect{}
	}
	jsonOK(w, redirects)
}

type createRedirectRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (s *Server) createRedirect(w http.ResponseWriter, r *http.Request) {
	var req createRedirectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.From == "" || req.To == "" {
		jsonError(w, "from and to are required", http.StatusBadRequest)
		return
	}

	redirect, err := s.store.CreateRedirect(req.From, req.To)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			jsonError(w, fmt.Sprintf("%s already has a redirect", req.From), http.StatusConflict)
			return
		}
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.syncCaddy(); err != nil {
		log.Printf("warning: caddy redirects file could not be written: %v", err)
	}

	log.Printf("redirect created: %s → %s", req.From, req.To)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, redirect)
}

func (s *Server) deleteRedirect(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}

	found, err := s.store.DeleteRedirect(id)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		jsonError(w, "redirect not found", http.StatusNotFound)
		return
	}

	if err := s.syncCaddy(); err != nil {
		log.Printf("warning: redirect deleted but caddy sync failed: %v", err)
	}

	log.Printf("redirect deleted: id=%d", id)
	jsonOK(w, map[string]string{"status": "deleted"})
}

// --- Volumes ---

func (s *Server) listVolumes(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	vols, err := s.store.ListVolumes(name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if vols == nil {
		vols = []store.Volume{}
	}
	jsonOK(w, vols)
}

func (s *Server) getVolume(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	vol, err := s.store.GetVolume(id)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if vol == nil {
		jsonError(w, "volume not found", http.StatusNotFound)
		return
	}
	jsonOK(w, vol)
}

type addVolumeRequest struct {
	Mount string `json:"mount"` // "/container/path" or "/host/path:/container/path"
}

func (s *Server) addVolume(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil || p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	var req addVolumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Mount == "" {
		jsonError(w, "mount is required", http.StatusBadRequest)
		return
	}

	hostPath, containerPath, managed := parseMount(name, req.Mount)
	if containerPath == "" || !strings.HasPrefix(containerPath, "/") {
		jsonError(w, "container path must be an absolute path", http.StatusBadRequest)
		return
	}

	if managed {
		if err := os.MkdirAll(hostPath, 0755); err != nil {
			jsonError(w, fmt.Sprintf("failed to create host directory: %v", err), http.StatusInternalServerError)
			return
		}
	}

	vol, err := s.store.CreateVolume(store.Volume{
		Project:       name,
		HostPath:      hostPath,
		ContainerPath: containerPath,
		Managed:       managed,
	})
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("volume added: %s id=%d host=%s container=%s managed=%v", name, vol.ID, hostPath, containerPath, managed)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, vol)
}

func (s *Server) removeVolume(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}

	purge := r.URL.Query().Get("data") == "delete"

	vol, err := s.store.GetVolume(id)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if vol == nil {
		jsonError(w, "volume not found", http.StatusNotFound)
		return
	}

	found, err := s.store.DeleteVolume(id)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		jsonError(w, "volume not found", http.StatusNotFound)
		return
	}

	resp := map[string]interface{}{"status": "removed", "host_path": vol.HostPath, "managed": vol.Managed}

	if purge && vol.Managed {
		if err := os.RemoveAll(vol.HostPath); err != nil {
			log.Printf("warning: failed to purge host data for volume %d (%s): %v", id, vol.HostPath, err)
			resp["purge_error"] = err.Error()
		} else {
			resp["purged"] = true
			log.Printf("volume purged: id=%d host=%s", id, vol.HostPath)
		}
	}

	log.Printf("volume removed: id=%d project=%s", id, vol.Project)
	jsonOK(w, resp)
}

// parseMount splits a mount spec into host path, container path, and managed flag.
// If only a container path is given (no ":"), the host path is auto-assigned under
// /var/lib/poof/<project>/ and managed is true.
func parseMount(project, mount string) (hostPath, containerPath string, managed bool) {
	if idx := strings.Index(mount, ":"); idx >= 0 {
		return mount[:idx], mount[idx+1:], false
	}
	containerPath = mount
	rel := strings.TrimPrefix(containerPath, "/")
	hostPath = "/var/lib/poof/" + project + "/" + rel
	return hostPath, containerPath, true
}

// syncCaddy regenerates the full Caddyfile from the current database state and
// pushes it to the Caddy admin API for a zero-downtime reload.
func (s *Server) syncCaddy() error {
	projects, err := s.store.ListProjects()
	if err != nil {
		return err
	}
	var running []store.Project
	for _, p := range projects {
		if docker.IsRunning(p.Name) {
			running = append(running, p)
		}
	}
	redirects, err := s.store.ListRedirects()
	if err != nil {
		return err
	}
	caddyfile := caddy.GenerateCaddyfile(running, redirects, s.cfg.Domain, s.cfg.PublicHost(), s.cfg.APIPort, s.cfg.CaddyStaticDir)
	return caddy.Reload(s.cfg.CaddyAdminURL, caddyfile)
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
