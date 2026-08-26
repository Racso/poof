package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/racso/poof/caddy"
	"github.com/racso/poof/defaults"
	"github.com/racso/poof/store"
)

// --- Settings helpers ---

func (s *Server) settingDomain() string {
	v, _ := s.store.GetSetting("domain")
	return v
}

func (s *Server) settingGitHubToken() string {
	v, _ := s.store.GetSetting("github-token")
	return v
}

func (s *Server) settingGitHubUser() string {
	v, _ := s.store.GetSetting("github-user")
	return v
}

// --- Config ---

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetAllSettings()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, settings)
}

func (s *Server) setConfig(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	validKeys := map[string]bool{"domain": true, "github-user": true, "github-token": true}
	if !validKeys[key] {
		jsonError(w, "unknown config key: "+key, http.StatusBadRequest)
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Value == "" {
		jsonError(w, "value is required", http.StatusBadRequest)
		return
	}
	if err := s.store.SetSetting(key, req.Value); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("config updated: %s", key)
	jsonOK(w, map[string]string{"key": key, "status": "updated"})
}

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
		running := false
		if p.IsStatic() {
			running = s.static.IsDeployed(s.cfg.DataDir, p.Name)
		} else {
			running = s.container.IsRunning(p.Name)
		}
		result = append(result, projectStatus{p, running})
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

	running := false
	if p.IsStatic() {
		running = s.static.IsDeployed(s.cfg.DataDir, name)
	} else {
		running = s.container.IsRunning(name)
	}

	snippet, _ := s.store.GetCaddySnippet(name)

	jsonOK(w, map[string]interface{}{
		"project":           p,
		"running":           running,
		"deployment":        last,
		"has_caddy_snippet": snippet != "",
	})
}

type createProjectRequest struct {
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Image    string `json:"image"`
	Repo     string `json:"repo"`
	Branch   string `json:"branch"`
	Port     int    `json:"port"`
	Subpath  string `json:"subpath"`
	Folder   string `json:"folder"`
	Static   string `json:"static"`
	Build    bool   `json:"build"`
	CI       *bool  `json:"ci"`
	CIMode   string `json:"ci_mode"`
	External string `json:"external"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// The project name becomes the container name, which Caddy resolves by
	// DNS — reject anything that wouldn't be a valid hostname.
	if err := store.ValidateProjectName(req.Name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Validate static mode.
	if req.Static != "" && req.Static != "static" && req.Static != "spa" {
		jsonError(w, "static must be empty, \"static\", or \"spa\"", http.StatusBadRequest)
		return
	}
	isStatic := req.Static == "static" || req.Static == "spa"

	// External projects route to a container Poof does not manage. They own a
	// domain and nothing else: no image, no repo, no CI, no deploys.
	var externalHost string
	if req.External != "" {
		if isStatic {
			jsonError(w, "--external and --static are mutually exclusive", http.StatusBadRequest)
			return
		}
		host, port, err := parseExternalTarget(req.External)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Refuse to register a route that points at nothing: the failure would
		// otherwise surface much later as a 502 with no obvious cause, and a
		// typo is by far the likeliest explanation. Existing is enough — a
		// stopped container still proves the name is real.
		if !s.container.ContainerExists(host) {
			jsonError(w, fmt.Sprintf(
				"container %q not found — check the name (it must already exist; it may be stopped)", host),
				http.StatusBadRequest)
			return
		}
		externalHost = host
		req.Port = port
		req.Image = ""
		req.Repo = ""
		ciOff := false
		req.CI = &ciOff
	}

	// Apply defaults
	if req.Domain == "" {
		req.Domain = req.Name + "." + s.settingDomain()
	}
	isExternal := externalHost != ""
	if !isStatic && !isExternal {
		if req.Image == "" {
			req.Image = fmt.Sprintf("ghcr.io/%s/%s", strings.ToLower(s.settingGitHubUser()), strings.ToLower(req.Name))
		}
		if req.Port == 0 {
			req.Port = defaults.Port
		}
	}
	// An external project has no repo to build from and no branch to track:
	// leave those empty rather than inventing defaults that mean nothing.
	if req.Repo == "" && !isExternal {
		req.Repo = fmt.Sprintf("%s/%s", s.settingGitHubUser(), req.Name)
	}
	if req.Branch == "" && !isExternal {
		req.Branch = defaults.Branch
	}

	// CI defaults to true.
	ci := true
	if req.CI != nil {
		ci = *req.CI
	}

	// ciMode defaults to managed; validate when set.
	ciMode := store.CIModeManaged
	if req.CIMode != "" {
		if req.CIMode != store.CIModeManaged && req.CIMode != store.CIModeCallable {
			jsonError(w, fmt.Sprintf("ci_mode must be %q or %q", store.CIModeManaged, store.CIModeCallable), http.StatusBadRequest)
			return
		}
		ciMode = req.CIMode
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

	// Get or create a deploy token for this repo.
	token, err := s.store.GetRepoToken(req.Repo)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if token == "" {
		token, err = generateToken()
		if err != nil {
			jsonError(w, "failed to generate token", http.StatusInternalServerError)
			return
		}
		if err := s.store.SetRepoToken(req.Repo, token); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	p := store.Project{
		Name:     req.Name,
		Domain:   req.Domain,
		Image:    req.Image,
		Repo:     req.Repo,
		Branch:   req.Branch,
		Port:     req.Port,
		Subpath:  req.Subpath,
		Folder:   req.Folder,
		Static:   req.Static,
		Build:    req.Build,
		CI:       ci,
		CIMode:   ciMode,
		External: externalHost,
	}

	if err := s.store.CreateProject(p); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Set up GitHub repo (secrets + workflow) if a PAT is configured and CI is enabled.
	if p.CI && s.settingGitHubToken() != "" {
		client := s.ghFactory(s.settingGitHubToken())
		owner, repoName, found := strings.Cut(req.Repo, "/")
		if !found {
			owner = s.settingGitHubUser()
			repoName = req.Repo
		}
		if err := client.SetRepoCI(owner, repoName, req.Name, s.cfg.PublicURL, token, req.Branch, req.Image, req.Folder, req.Static, p.CIMode, req.Build); err != nil {
			log.Printf("warning: GitHub setup for %s failed: %v", req.Name, err)
		}
	}

	// An external project is live the moment it is registered: there is no
	// deploy step to wire up its network or publish its route.
	if p.IsExternal() {
		if _, err := s.ensureProjectNetwork(&p); err != nil {
			log.Printf("warning: network setup for external project %s: %v", p.Name, err)
		}
		if err := s.syncCaddy(); err != nil {
			log.Printf("warning: caddy sync after creating %s: %v", p.Name, err)
		}
		log.Printf("external project created: %s → %s", p.Name, p.Upstream())
		w.WriteHeader(http.StatusCreated)
		jsonOK(w, p)
		return
	}

	log.Printf("project created: %s (repo=%s branch=%s image=%s static=%s build=%v ci=%v mode=%s)", p.Name, p.Repo, p.Branch, p.Image, p.Static, p.Build, p.CI, p.CIMode)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, p)
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

	var patch map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	ciChanged, ciModeChanged := false, false
	repoChanged, branchChanged, folderChanged, staticChanged, buildChanged := false, false, false, false, false

	for key, val := range patch {
		switch key {
		case "domain":
			if v, ok := val.(string); ok && v != "" {
				p.Domain = v
			}
		case "image":
			if v, ok := val.(string); ok && v != "" {
				p.Image = v
			}
		case "repo":
			if v, ok := val.(string); ok && v != "" {
				repoChanged = v != p.Repo
				p.Repo = v
			}
		case "branch":
			if v, ok := val.(string); ok && v != "" {
				branchChanged = v != p.Branch
				p.Branch = v
			}
		case "port":
			if v, ok := val.(float64); ok && v != 0 {
				p.Port = int(v)
			}
		case "subpath":
			if v, ok := val.(string); ok && v != "" {
				if !validSubpath(v) {
					jsonError(w, "subpath must be disabled, redirect, or proxy", http.StatusBadRequest)
					return
				}
				p.Subpath = v
			}
		case "folder":
			if v, ok := val.(string); ok {
				folderChanged = v != p.Folder
				p.Folder = v
			}
		case "static":
			if v, ok := val.(string); ok && v != "" {
				if v != "static" && v != "spa" && v != "container" {
					jsonError(w, "static must be \"static\", \"spa\", or \"container\"", http.StatusBadRequest)
					return
				}
				staticChanged = v != p.Static && (v != "container" || p.Static != "")
				if v == "container" {
					p.Static = ""
				} else {
					p.Static = v
				}
			}
		case "build":
			if v, ok := val.(bool); ok {
				buildChanged = v != p.Build
				p.Build = v
			}
		case "ci":
			if v, ok := val.(bool); ok {
				ciChanged = v != p.CI
				p.CI = v
			}
		case "ci_mode":
			if v, ok := val.(string); ok && v != "" {
				if v != store.CIModeManaged && v != store.CIModeCallable {
					jsonError(w, fmt.Sprintf("ci_mode must be %q or %q", store.CIModeManaged, store.CIModeCallable), http.StatusBadRequest)
					return
				}
				ciModeChanged = v != p.CIMode
				p.CIMode = v
			}
		}
	}

	// If switching from container to static, stop the container and clear
	// container-specific fields.
	if staticChanged && p.IsStatic() {
		p.Image = ""
		p.Port = 0
		if err := s.container.Stop(name); err != nil {
			log.Printf("warning: stopping container for %s during static conversion: %v", name, err)
		}
	}
	// If switching from static to container, clean up static files.
	if staticChanged && !p.IsStatic() {
		s.static.Remove(s.cfg.DataDir, name)
	}

	if err := s.store.UpdateProject(*p); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("project updated: %s", name)
	if s.settingGitHubToken() != "" && (repoChanged || branchChanged || folderChanged || staticChanged || buildChanged || ciChanged || ciModeChanged) {
		client := s.ghFactory(s.settingGitHubToken())
		owner, repoName, found := strings.Cut(p.Repo, "/")
		if !found {
			owner = s.settingGitHubUser()
			repoName = p.Repo
		}
		repoToken, _ := s.store.GetRepoToken(p.Repo)
		ciSiblings, _ := s.store.CountCIEnabledProjectsForRepo(p.Repo, p.Name)
		if err := client.RefreshProjectCI(owner, repoName, p.Name, p.CI, s.cfg.PublicURL, repoToken, p.Branch, p.Image, p.Folder, p.Static, p.CIMode, p.Build, ciSiblings == 0); err != nil {
			log.Printf("warning: GitHub CI refresh for %s failed: %v", name, err)
		}
		if !p.CI && ciSiblings == 0 {
			_ = s.store.DeleteRepoToken(p.Repo)
		}
	}

	if err := s.syncCaddy(); err != nil {
		log.Printf("warning: caddy sync after update failed: %v", err)
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

	// Stop container or clean up static files.
	switch {
	case p.IsStatic():
		s.static.Remove(s.cfg.DataDir, name)
	case p.IsExternal():
		// The upstream container belongs to whoever created it. Remove the
		// route and the network Poof made; leave the container running.
		s.teardownAppNetwork(name)
		log.Printf("external project removed: %s (container %s left untouched)", name, p.External)
	default:
		if err := s.container.Stop(name); err != nil {
			log.Printf("warning: stopping container for %s: %v", name, err)
		}
		s.teardownAppNetwork(name)
	}

	// Clean up GitHub if PAT is configured.
	if s.settingGitHubToken() != "" {
		client := s.ghFactory(s.settingGitHubToken())
		owner, repoName, found := strings.Cut(p.Repo, "/")
		if !found {
			owner = s.settingGitHubUser()
			repoName = p.Repo
		}
		// Only delete the POOF_TOKEN secret if this is the last project for this repo.
		siblings, _ := s.store.CountProjectsForRepo(p.Repo)
		lastForRepo := siblings <= 1
		if err := client.RemoveRepoCI(owner, repoName, name, p.Branch, lastForRepo); err != nil {
			log.Printf("warning: GitHub cleanup for %s: %v", name, err)
		}
		if lastForRepo {
			_ = s.store.DeleteRepoToken(p.Repo)
		}
	}

	// network_members has no foreign key on member (a member may be a
	// container Poof doesn't own), so clean up explicitly.
	if err := s.store.DeleteNetworkMembersForProject(name); err != nil {
		log.Printf("warning: removing network memberships for %s: %v", name, err)
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

	resp := map[string]string{"status": "deleted"}
	if p.IsExternal() {
		// Say plainly what was and wasn't destroyed — the container is not ours.
		resp["note"] = fmt.Sprintf(
			"removed the route and network; container %q was left running (Poof does not manage it)",
			p.External)
	}
	jsonOK(w, resp)
}

type cloneRequest struct {
	Suffix  string   `json:"suffix"`
	EnvKeys []string `json:"env_keys,omitempty"`
	// Caddy decides what to do with the source's Caddy snippet when one exists.
	// "yes" copies it verbatim; "no" skips it. Empty means "undecided" and
	// the request is refused when a snippet is present, forcing the operator
	// to pick explicitly so they don't forget to fix container references.
	Caddy string `json:"caddy,omitempty"`
}

func (s *Server) cloneProject(w http.ResponseWriter, r *http.Request) {
	sourceName := r.PathValue("name")
	source, err := s.store.GetProject(sourceName)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if source == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	var req cloneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Suffix == "" {
		jsonError(w, "suffix is required", http.StatusBadRequest)
		return
	}
	switch req.Caddy {
	case "", "yes", "no":
	default:
		jsonError(w, `caddy must be "yes", "no", or omitted`, http.StatusBadRequest)
		return
	}

	cloneName := sourceName + "-" + req.Suffix
	if err := store.ValidateProjectName(cloneName); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check duplicate.
	if existing, _ := s.store.GetProject(cloneName); existing != nil {
		jsonError(w, "project already exists: "+cloneName, http.StatusConflict)
		return
	}

	// Refuse to clone if the source has a Caddy snippet and the caller
	// hasn't made an explicit choice. Snippets typically reference the
	// source's container name (e.g. poof-<source>) which won't match the
	// clone — silently copying would produce a broken clone, silently
	// skipping would lose routing logic.
	sourceSnippet, err := s.store.GetCaddySnippet(sourceName)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sourceSnippet != "" && req.Caddy == "" {
		jsonError(w,
			"source has a Caddy snippet; pass --caddy-yes (copy verbatim) "+
				"or --caddy-no (skip) to decide what the clone should do with it",
			http.StatusPreconditionRequired)
		return
	}

	// Get or create repo token.
	token, err := s.store.GetRepoToken(source.Repo)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if token == "" {
		token, err = generateToken()
		if err != nil {
			jsonError(w, "failed to generate token", http.StatusInternalServerError)
			return
		}
		if err := s.store.SetRepoToken(source.Repo, token); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Derive clone config from source. Domain is left for the server default.
	p := store.Project{
		Name:    cloneName,
		Domain:  cloneName + "." + s.settingDomain(),
		Image:   source.Image,
		Repo:    source.Repo,
		Branch:  req.Suffix,
		Port:    source.Port,
		Subpath: source.Subpath,
		Folder:  source.Folder,
		Static:  source.Static,
		Build:   source.Build,
		CI:      source.CI,
		CIMode:  source.CIMode,
	}

	if err := s.store.CreateProject(p); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Copy env vars if requested.
	var copiedKeys []string
	if len(req.EnvKeys) > 0 {
		copiedKeys, err = s.store.CopyEnvVars(sourceName, cloneName, req.EnvKeys)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Copy Caddy snippet verbatim if requested.
	caddySnippetCopied := false
	if sourceSnippet != "" && req.Caddy == "yes" {
		if err := s.store.SetCaddySnippet(cloneName, sourceSnippet); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		caddySnippetCopied = true
	}

	// Set up GitHub.
	if p.CI && s.settingGitHubToken() != "" {
		client := s.ghFactory(s.settingGitHubToken())
		owner, repoName, found := strings.Cut(source.Repo, "/")
		if !found {
			owner = s.settingGitHubUser()
			repoName = source.Repo
		}
		if err := client.SetRepoCI(owner, repoName, cloneName, s.cfg.PublicURL, token, p.Branch, p.Image, p.Folder, p.Static, p.CIMode, p.Build); err != nil {
			log.Printf("warning: GitHub setup for %s failed: %v", cloneName, err)
		}
	}

	log.Printf("project cloned: %s → %s (branch=%s)", sourceName, cloneName, req.Suffix)

	result := map[string]interface{}{"project": p}
	if copiedKeys != nil {
		result["env_keys_copied"] = copiedKeys
	}
	if caddySnippetCopied {
		result["caddy_snippet_copied"] = true
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, result)
}

func (s *Server) refreshProject(w http.ResponseWriter, r *http.Request) {
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

	if s.settingGitHubToken() == "" {
		jsonError(w, "no GitHub PAT configured on server", http.StatusPreconditionFailed)
		return
	}

	repoToken, _ := s.store.GetRepoToken(p.Repo)
	if p.CI && repoToken == "" {
		jsonError(w, "no deploy token found for repo "+p.Repo, http.StatusPreconditionFailed)
		return
	}

	client := s.ghFactory(s.settingGitHubToken())
	owner, repoName, found := strings.Cut(p.Repo, "/")
	if !found {
		owner = s.settingGitHubUser()
		repoName = p.Repo
	}

	ciSiblings, _ := s.store.CountCIEnabledProjectsForRepo(p.Repo, p.Name)
	if err := client.RefreshProjectCI(owner, repoName, p.Name, p.CI, s.cfg.PublicURL, repoToken, p.Branch, p.Image, p.Folder, p.Static, p.CIMode, p.Build, ciSiblings == 0); err != nil {
		jsonError(w, fmt.Sprintf("GitHub CI refresh failed: %v", err), http.StatusInternalServerError)
		return
	}
	if !p.CI && ciSiblings == 0 {
		_ = s.store.DeleteRepoToken(p.Repo)
	}

	status := "refreshed"
	if !p.CI {
		status = "ci removed"
	}
	log.Printf("project CI refreshed: %s (status=%s)", name, status)
	jsonOK(w, map[string]string{"status": status})
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

	if p.IsStatic() {
		jsonError(w, "this is a static project — use POST /projects/"+name+"/deploy/static", http.StatusBadRequest)
		return
	}
	if p.IsExternal() {
		jsonError(w, externalRefusal(p), http.StatusBadRequest)
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

func (s *Server) deployStaticProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil || p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	if !p.IsStatic() {
		jsonError(w, "this is not a static project", http.StatusBadRequest)
		return
	}

	// Limit upload size (500MB).
	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)

	// Static GC prunes version directories; hold the gate so it cannot run
	// against a half-written one.
	s.gate.enterDeploy()
	defer s.gate.leaveDeploy()

	depID, _ := s.store.RecordDeployment(name, "static", "running")

	if err := s.static.Deploy(s.cfg.DataDir, name, depID, r.Body); err != nil {
		s.store.UpdateDeploymentStatus(depID, "failed")
		jsonError(w, fmt.Sprintf("static deploy failed: %v", err), http.StatusInternalServerError)
		return
	}

	s.store.UpdateDeploymentStatus(depID, "success")
	log.Printf("static deployed: %s (v%d)", name, depID)

	if err := s.syncCaddy(); err != nil {
		log.Printf("warning: caddy sync after static deploy failed: %v", err)
	}

	jsonOK(w, map[string]interface{}{
		"status": "deployed",
		"domain": p.Domain,
	})
}

func (s *Server) rollbackProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil || p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}
	if p.IsExternal() {
		jsonError(w, externalRefusal(p), http.StatusBadRequest)
		return
	}

	prev, err := s.store.PreviousDeployment(name)
	if err != nil || prev == nil {
		jsonError(w, "no previous deployment to roll back to", http.StatusBadRequest)
		return
	}

	if p.IsStatic() {
		log.Printf("static rollback triggered: %s → v%d", name, prev.ID)
		if err := s.static.Rollback(s.cfg.DataDir, name, prev.ID); err != nil {
			jsonError(w, fmt.Sprintf("rollback failed: %v", err), http.StatusInternalServerError)
			return
		}
		if err := s.syncCaddy(); err != nil {
			log.Printf("warning: caddy sync after rollback failed: %v", err)
		}
		jsonOK(w, map[string]interface{}{
			"status": "rolled back",
			"domain": p.Domain,
		})
		return
	}

	log.Printf("rollback triggered: %s → %s", name, prev.Image)
	s.runDeploy(w, p, prev.Image)
}

// --- Pause & Resume & Snapshot ---

// pauseProject takes a project offline: the pause flag flips, Caddy re-syncs
// so every route answers 503, and the container is stopped (not removed — its
// writable layer is kept for investigation, and resume restores the identical
// container). The registration (config, env vars, snippet) is untouched.
// Failures are hard errors — pause is incident response, so the caller must
// know when the site is not actually offline yet. The flag stays persisted
// either way, so retrying converges.
func (s *Server) pauseProject(w http.ResponseWriter, r *http.Request) {
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
	if p.Paused {
		jsonOK(w, map[string]string{"status": "already paused", "domain": p.Domain})
		return
	}

	if err := s.store.SetProjectPaused(name, true); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.syncCaddy(); err != nil {
		jsonError(w, fmt.Sprintf("project marked paused but routing update failed: %v — retry the command", err), http.StatusInternalServerError)
		return
	}
	if !p.IsStatic() {
		if err := s.container.Suspend(name); err != nil {
			jsonError(w, fmt.Sprintf("paused (routing is offline) but stopping the container failed: %v — retry the command", err), http.StatusInternalServerError)
			return
		}
	}

	log.Printf("project paused: %s", name)
	jsonOK(w, map[string]string{"status": "paused", "domain": p.Domain})
}

// resumeProject puts a paused project back online: the container is started
// (a no-op for static projects or when none was ever deployed), the flag
// clears, and Caddy re-syncs to the exact pre-pause routing. If a deploy
// happened while paused, its container was created but never started — that
// deployment row sits at "staged" and is resolved here to success/failed by
// the start outcome. A start failure does NOT re-pause: same as pushing a
// broken image, the project ends up unpaused with the backend down, loudly.
func (s *Server) resumeProject(w http.ResponseWriter, r *http.Request) {
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
	if !p.Paused {
		jsonOK(w, map[string]string{"status": "already resumed", "domain": p.Domain})
		return
	}

	var startErr error
	if !p.IsStatic() {
		startErr = s.container.Start(name)
	}

	if err := s.store.SetProjectPaused(name, false); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if last, _ := s.store.LastDeployment(name); last != nil && last.Status == "staged" {
		status := "success"
		if startErr != nil {
			status = "failed"
		}
		if err := s.store.UpdateDeploymentStatus(last.ID, status); err != nil {
			log.Printf("warning: resolving staged deployment %d for %s: %v", last.ID, name, err)
		}
	}

	if err := s.syncCaddy(); err != nil {
		log.Printf("warning: caddy sync after resume failed: %v", err)
	}

	if startErr != nil {
		jsonError(w, fmt.Sprintf("resumed, but the container failed to start: %v", startErr), http.StatusInternalServerError)
		return
	}

	log.Printf("project resumed: %s", name)
	jsonOK(w, map[string]string{"status": "resumed", "domain": p.Domain})
}

// snapshotProject preserves the project's container for forensics: writable
// layer committed to a local image, logs + inspect dumped to disk. Works on
// paused (stopped) containers; intended flow is pause → snapshot → fix.
func (s *Server) snapshotProject(w http.ResponseWriter, r *http.Request) {
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
	if p.IsStatic() {
		jsonError(w, "static projects have no container to snapshot", http.StatusBadRequest)
		return
	}
	if p.IsExternal() {
		jsonError(w, externalRefusal(p), http.StatusBadRequest)
		return
	}

	res, err := s.container.Snapshot(name, s.cfg.DataDir)
	if err != nil {
		jsonError(w, fmt.Sprintf("snapshot failed: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("project snapshotted: %s → %s", name, res.ImageRef)
	jsonOK(w, map[string]string{"status": "snapshotted", "image": res.ImageRef, "dir": res.Dir})
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

	projNets, err := s.store.ListNetworksForProject(p.Name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nets := make([]string, 0, len(projNets))
	for _, pn := range projNets {
		// Ensure each attached network exists before (re)deploying, recreating
		// it with the right internal flag if it was removed out-of-band.
		internal := false
		if def, derr := s.store.GetNetwork(pn.Network); derr == nil && def != nil {
			internal = def.Internal
		}
		if err := s.container.EnsureNetwork(pn.Network, internal); err != nil {
			jsonError(w, fmt.Sprintf("ensure network %q: %v", pn.Network, err), http.StatusInternalServerError)
			return
		}
		nets = append(nets, pn.Network)
	}

	// The project's own network: created on first deploy, Caddy attached for
	// routing. The container's only automatic neighbor is Caddy.
	appNet, err := s.ensureProjectNetwork(p)
	if err != nil {
		jsonError(w, fmt.Sprintf("ensure project network: %v", err), http.StatusInternalServerError)
		return
	}

	// Hold the deploy gate across the pull: garbage collection frees layers
	// host-wide and would otherwise pull them out from under it.
	s.gate.enterDeploy()
	defer s.gate.leaveDeploy()

	log.Printf("deploy started: %s → %s", p.Name, image)
	depID, _ := s.store.RecordDeployment(p.Name, image, "running")

	// While paused, the new container is created but not started: the fix is
	// applied offline and resume brings it up. Its deployment row stays
	// "staged" until resume resolves it to success/failed by the start outcome.
	err = s.container.Deploy(ContainerDeployConfig{
		Name:          p.Name,
		Image:         image,
		EnvVars:       envVars,
		Volumes:       mounts,
		Network:       appNet,
		Networks:      nets,
		RegistryUser:  s.settingGitHubUser(),
		RegistryToken: s.settingGitHubToken(),
		CreateOnly:    p.Paused,
	})

	if err != nil {
		s.store.UpdateDeploymentStatus(depID, "failed")
		log.Printf("deploy failed: %s → %v", p.Name, err)
		jsonError(w, fmt.Sprintf("deploy failed: %v", err), http.StatusInternalServerError)
		return
	}

	if p.Paused {
		s.store.UpdateDeploymentStatus(depID, "staged")
		log.Printf("staged %s → %s (paused; container created, not started)", p.Name, image)
		jsonOK(w, map[string]interface{}{
			"status": "staged",
			"image":  image,
			"domain": p.Domain,
			"note":   "project is paused — container replaced but not started; `poof resume` to start it",
		})
		return
	}

	s.store.UpdateDeploymentStatus(depID, "success")
	log.Printf("deployed %s → %s", p.Name, image)

	if err := s.syncCaddy(); err != nil {
		log.Printf("warning: caddy sync after deploy failed: %v", err)
	}

	s.requestAutoGC()

	jsonOK(w, map[string]interface{}{
		"status": "deployed",
		"image":  image,
		"domain": p.Domain,
	})
}

// --- Logs ---

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	p, _ := s.store.GetProject(name)
	if p != nil && p.IsStatic() {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("no container logs for static projects\n"))
		return
	}

	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			lines = n
		}
	}

	logs, err := s.container.Logs(name, lines)
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

func (s *Server) copyEnv(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("name")
	target := r.PathValue("target")

	// Verify target project exists.
	tp, err := s.store.GetProject(target)
	if err != nil || tp == nil {
		jsonError(w, "target project not found: "+target, http.StatusNotFound)
		return
	}

	var req struct {
		Keys []string `json:"keys"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}

	copied, err := s.store.CopyEnvVars(source, target, req.Keys)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("env copy: %s → %s (%d key(s))", source, target, len(copied))
	jsonOK(w, map[string]interface{}{"status": "copied", "keys": copied})
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

	if p.IsStatic() {
		jsonError(w, "volumes are not supported for static projects", http.StatusBadRequest)
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

// --- Networks ---

func (s *Server) listNetworks(w http.ResponseWriter, r *http.Request) {
	nets, err := s.store.ListNetworks()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if nets == nil {
		nets = []store.Network{}
	}
	jsonOK(w, nets)
}

type createNetworkRequest struct {
	Name     string `json:"name"`
	Internal bool   `json:"internal"`
}

func (s *Server) createNetwork(w http.ResponseWriter, r *http.Request) {
	var req createNetworkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}

	if req.Name == ControlPlaneNetwork {
		jsonError(w, controlPlaneRefusal(), http.StatusBadRequest)
		return
	}

	if existing, err := s.store.GetNetwork(req.Name); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	} else if existing != nil {
		jsonError(w, fmt.Sprintf("network %q already exists", req.Name), http.StatusConflict)
		return
	}

	if err := s.container.EnsureNetwork(req.Name, req.Internal); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	net, err := s.store.CreateNetwork(store.Network{Name: req.Name, Internal: req.Internal})
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("network created: %s internal=%v", net.Name, net.Internal)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, net)
}

func (s *Server) deleteNetwork(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	count, err := s.store.CountNetworkMembers(name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if count > 0 {
		jsonError(w, fmt.Sprintf("network %q is attached to %d project(s); detach them first", name, count), http.StatusConflict)
		return
	}

	found, err := s.store.DeleteNetwork(name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		jsonError(w, "network not found", http.StatusNotFound)
		return
	}

	// The Docker network is left in place; removing it is a separate, explicit
	// op (it may hold non-Poof endpoints). We only drop the Poof record.
	log.Printf("network record removed: %s", name)
	jsonOK(w, map[string]interface{}{"status": "removed", "name": name})
}

func (s *Server) listProjectNetworks(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	nets, err := s.store.ListNetworksForProject(name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if nets == nil {
		nets = []store.NetworkMember{}
	}
	jsonOK(w, nets)
}

// netMembersRequest is the body of POST/DELETE /networks/{name}/members.
// Members are named containers or projects; caddy and poof are flags because
// there is only ever one of each.
type netMembersRequest struct {
	Members []string `json:"members"`
	Caddy   bool     `json:"caddy"`
	Poof    bool     `json:"poof"`
}

// resolveMemberKind decides whether a name refers to a Poof project or to a
// container Poof does not manage. Projects win: their membership is re-applied
// on every deploy, which is the stronger guarantee.
func (s *Server) resolveMemberKind(name string) string {
	if p, err := s.store.GetProject(name); err == nil && p != nil {
		return store.MemberProject
	}
	return store.MemberContainer
}

// addNetworkMembers attaches projects, containers, Caddy and/or the daemon to
// a network, then reconciles so the change takes effect immediately rather
// than at the next deploy.
func (s *Server) addNetworkMembers(w http.ResponseWriter, r *http.Request) {
	network := r.PathValue("name")
	if network == ControlPlaneNetwork {
		jsonError(w, controlPlaneRefusal(), http.StatusBadRequest)
		return
	}
	def, err := s.store.GetNetwork(network)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if def == nil {
		jsonError(w, fmt.Sprintf("network %q does not exist; create it with 'poof net create %s'", network, network), http.StatusBadRequest)
		return
	}

	var req netMembersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Members) == 0 && !req.Caddy && !req.Poof {
		jsonError(w, "specify at least one member, or --caddy / --poof", http.StatusBadRequest)
		return
	}

	var added []store.NetworkMember
	for _, name := range req.Members {
		if name == "" {
			continue
		}
		kind := s.resolveMemberKind(name)
		if kind == store.MemberProject {
			if p, _ := s.store.GetProject(name); p != nil && p.IsStatic() {
				jsonError(w, fmt.Sprintf("project %q is static: it has no container to attach", name), http.StatusBadRequest)
				return
			}
		}
		m, err := s.store.AddNetworkMember(network, name, kind)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		added = append(added, *m)
	}
	for _, special := range []struct {
		want bool
		kind string
	}{{req.Caddy, store.MemberCaddy}, {req.Poof, store.MemberPoof}} {
		if !special.want {
			continue
		}
		m, err := s.store.AddNetworkMember(network, special.kind, special.kind)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		added = append(added, *m)
	}

	// Apply now: attaching a live container needs no recreate.
	s.reconcileNetworkMembers()
	log.Printf("network members added: network=%s count=%d", network, len(added))
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]interface{}{"network": network, "added": added})
}

// removeNetworkMembers detaches members. It updates desired state and detaches
// the running containers; a member that was never attached is not an error.
func (s *Server) removeNetworkMembers(w http.ResponseWriter, r *http.Request) {
	network := r.PathValue("name")

	var req netMembersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Members) == 0 && !req.Caddy && !req.Poof {
		jsonError(w, "specify at least one member, or --caddy / --poof", http.StatusBadRequest)
		return
	}

	targets := map[string]string{} // member -> kind
	for _, name := range req.Members {
		if name == "" {
			continue
		}
		// Try both kinds: the caller names a thing, not a classification.
		for _, kind := range []string{store.MemberProject, store.MemberContainer} {
			if m, _ := s.store.GetNetworkMember(network, name, kind); m != nil {
				targets[name] = kind
			}
		}
	}
	if req.Caddy {
		targets[store.MemberCaddy] = store.MemberCaddy
	}
	if req.Poof {
		targets[store.MemberPoof] = store.MemberPoof
	}

	var removed []string
	for member, kind := range targets {
		ok, err := s.store.RemoveNetworkMember(network, member, kind)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			continue
		}
		removed = append(removed, member)
		// Detach the live container too, so the change is immediate.
		if container, ok := s.resolveMemberContainer(store.NetworkMember{
			Network: network, Member: member, Kind: kind,
		}); ok {
			if err := s.container.DisconnectNetwork(network, container); err != nil {
				log.Printf("warning: detaching %s from %s: %v", container, network, err)
			}
		}
	}

	log.Printf("network members removed: network=%s count=%d", network, len(removed))
	jsonOK(w, map[string]interface{}{"network": network, "removed": removed})
}

// controlPlaneRefusal explains why poof-net is off limits and what to do
// instead — a bare refusal would just invite `docker network connect`.
func controlPlaneRefusal() string {
	return fmt.Sprintf(
		"%q is Poof's control plane (Caddy + the daemon) and cannot take members. "+
			"Projects each get their own network automatically. For a shared network, "+
			"create one and attach what it needs: "+
			"poof net create <name> && poof net add <name> <member...> [--caddy] [--poof]",
		ControlPlaneNetwork)
}

// listNetworkMembers reports everything attached to a network.
func (s *Server) listNetworkMembers(w http.ResponseWriter, r *http.Request) {
	network := r.PathValue("name")
	def, err := s.store.GetNetwork(network)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if def == nil {
		jsonError(w, "network not found", http.StatusNotFound)
		return
	}
	members, err := s.store.ListNetworkMembers(network)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if members == nil {
		members = []store.NetworkMember{}
	}
	jsonOK(w, members)
}

// --- Caddy Snippets ---

const caddySnippetHeader = "# [poof-caddy] hash:sha256:"

func (s *Server) listCaddySnippets(w http.ResponseWriter, r *http.Request) {
	snippets, err := s.store.GetAllCaddySnippets()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return just the project names that have snippets.
	names := make([]string, 0, len(snippets))
	for name := range snippets {
		names = append(names, name)
	}
	sort.Strings(names)
	jsonOK(w, names)
}

// snippetHash computes the SHA-256 hex digest of the given content.
func snippetHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

func (s *Server) getCaddySnippet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil || p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	content, err := s.store.GetCaddySnippet(name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return the snippet with the hash header prepended.
	hash := snippetHash(content)
	body := caddySnippetHeader + hash + "\n" + content

	jsonOK(w, map[string]interface{}{
		"content": body,
	})
}

func (s *Server) setCaddySnippet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil || p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20)) // 1 MB limit
	if err != nil {
		jsonError(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}

	var req struct {
		Content string `json:"content"`
		Force   bool   `json:"force"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	raw := req.Content

	// Extract hash from header line.
	var receivedHash string
	if strings.HasPrefix(raw, caddySnippetHeader) {
		firstNL := strings.Index(raw, "\n")
		if firstNL == -1 {
			receivedHash = raw[len(caddySnippetHeader):]
			raw = ""
		} else {
			receivedHash = raw[len(caddySnippetHeader):firstNL]
			raw = raw[firstNL+1:]
		}
	} else if !req.Force {
		jsonError(w, "missing hash header — use --force to push without concurrency check", http.StatusConflict)
		return
	}

	// Concurrency check: compare received hash with current content hash.
	if !req.Force && receivedHash != "" {
		current, err := s.store.GetCaddySnippet(name)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		currentHash := snippetHash(current)
		if receivedHash != currentHash {
			jsonError(w, "conflict: snippet was modified since you last pulled it — pull again or use --force", http.StatusConflict)
			return
		}
	}

	if err := s.store.SetCaddySnippet(name, raw); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("caddy snippet updated: %s", name)
	if err := s.syncCaddy(); err != nil {
		log.Printf("warning: caddy sync after snippet update failed: %v", err)
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) deleteCaddySnippet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.store.GetProject(name)
	if err != nil || p == nil {
		jsonError(w, "project not found", http.StatusNotFound)
		return
	}

	deleted, err := s.store.DeleteCaddySnippet(name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !deleted {
		jsonError(w, "no caddy snippet for this project", http.StatusNotFound)
		return
	}

	log.Printf("caddy snippet deleted: %s", name)
	if err := s.syncCaddy(); err != nil {
		log.Printf("warning: caddy sync after snippet delete failed: %v", err)
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

// diagnoseWorkflowMigration walks every project and reports whether its
// GitHub workflow file lives at the legacy path (poof-<name>.yml) or the
// canonical v0.16.0+ path (poof-auto-ci-<name>.yml), plus any other
// workflow files in the same repo that still `uses:` the legacy path.
//
// Read-only — never modifies anything on GitHub. The CLI consumes this
// to render `poof migrate workflows` output. Skips projects with CI
// disabled (no Poof workflow file expected) and projects in repos
// outside the configured GitHub user when no PAT is set.
func (s *Server) diagnoseWorkflowMigration(w http.ResponseWriter, r *http.Request) {
	if s.settingGitHubToken() == "" {
		jsonError(w, "no GitHub PAT configured on server", http.StatusPreconditionFailed)
		return
	}
	projects, err := s.store.ListProjects()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	client := s.ghFactory(s.settingGitHubToken())
	diagnostics := make([]any, 0, len(projects))
	for _, p := range projects {
		owner, repoName, found := strings.Cut(p.Repo, "/")
		if !found {
			owner = s.settingGitHubUser()
			repoName = p.Repo
		}
		d, err := client.WorkflowMigrationDiagnostic(owner, repoName, p.Name, p.Branch, p.CI)
		if err != nil {
			// Surface the project entry with an error string and keep going —
			// one bad project shouldn't sink the whole report.
			diagnostics = append(diagnostics, map[string]any{
				"project": p.Name,
				"repo":    p.Repo,
				"ci":      p.CI,
				"error":   err.Error(),
			})
			continue
		}
		diagnostics = append(diagnostics, d)
	}
	jsonOK(w, map[string]any{"diagnostics": diagnostics})
}

// applyWorkflowMigration renames the GitHub workflow file from the
// pre-v0.16.0 path (poof-<name>.yml) to the canonical
// poof-auto-ci-<name>.yml for the requested set of projects.
//
// Filter (request body):
//   - {} (empty)              → every project (caller must opt in via --all on the CLI)
//   - {"project": "name"}     → just that project
//   - {"repo": "Owner/repo"}  → every project in that repo
//
// For each in-scope project the handler regenerates the workflow at the
// new path (reusing the SetRepoCI flow that already targets the new
// path post-v0.16.0) and then deletes the file at the legacy path.
// Idempotent: re-running on a project that's already at the new path
// is a no-op.
func (s *Server) applyWorkflowMigration(w http.ResponseWriter, r *http.Request) {
	if s.settingGitHubToken() == "" {
		jsonError(w, "no GitHub PAT configured on server", http.StatusPreconditionFailed)
		return
	}

	var req struct {
		Project string `json:"project"`
		Repo    string `json:"repo"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
	}

	all, err := s.store.ListProjects()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Apply filter. If both project and repo are given, project wins
	// (more specific).
	var targets []store.Project
	switch {
	case req.Project != "":
		for _, p := range all {
			if p.Name == req.Project {
				targets = append(targets, p)
				break
			}
		}
		if len(targets) == 0 {
			jsonError(w, fmt.Sprintf("project %q not found", req.Project), http.StatusNotFound)
			return
		}
	case req.Repo != "":
		for _, p := range all {
			if p.Repo == req.Repo {
				targets = append(targets, p)
			}
		}
		if len(targets) == 0 {
			jsonError(w, fmt.Sprintf("no projects in repo %q", req.Repo), http.StatusNotFound)
			return
		}
	default:
		targets = all
	}

	client := s.ghFactory(s.settingGitHubToken())
	results := make([]map[string]any, 0, len(targets))

	for _, p := range targets {
		owner, repoName, found := strings.Cut(p.Repo, "/")
		if !found {
			owner = s.settingGitHubUser()
			repoName = p.Repo
		}

		entry := map[string]any{
			"project":  p.Name,
			"repo":     p.Repo,
			"old_path": fmt.Sprintf(".github/workflows/poof-%s.yml", p.Name),
			"new_path": fmt.Sprintf(".github/workflows/poof-auto-ci-%s.yml", p.Name),
		}

		if !p.CI {
			entry["status"] = "skipped"
			entry["reason"] = "ci_disabled"
			results = append(results, entry)
			continue
		}

		// Pre-flight: if the legacy path doesn't exist, there's nothing
		// to migrate — skip without touching the new path either, since
		// it's likely already migrated and a refresh would be busywork.
		diag, derr := client.WorkflowMigrationDiagnostic(owner, repoName, p.Name, p.Branch, p.CI)
		if derr != nil {
			entry["status"] = "error"
			entry["error"] = fmt.Sprintf("diagnostic: %v", derr)
			results = append(results, entry)
			continue
		}
		if !diag.OldPathExists {
			entry["status"] = "skipped"
			if diag.NewPathExists {
				entry["reason"] = "already_migrated"
			} else {
				entry["reason"] = "no_legacy_workflow"
			}
			results = append(results, entry)
			continue
		}

		// Step 1: write the workflow at the new path (post-v0.16.0
		// SetRepoCI already targets the canonical path).
		repoToken, _ := s.store.GetRepoToken(p.Repo)
		if err := client.SetRepoCI(owner, repoName, p.Name, s.cfg.PublicURL, repoToken, p.Branch, p.Image, p.Folder, p.Static, p.CIMode, p.Build); err != nil {
			entry["status"] = "error"
			entry["error"] = fmt.Sprintf("write new path: %v", err)
			results = append(results, entry)
			continue
		}

		// Step 2: delete the legacy file. If this fails we end up in
		// "partial" state, which the diagnostic detects and a re-run
		// can clean up. Not fatal.
		if err := client.DeleteLegacyWorkflow(owner, repoName, p.Name); err != nil {
			entry["status"] = "partial"
			entry["error"] = fmt.Sprintf("delete legacy: %v", err)
			results = append(results, entry)
			continue
		}

		entry["status"] = "renamed"
		results = append(results, entry)
		log.Printf("workflow migrated: %s (%s → %s)", p.Name, entry["old_path"], entry["new_path"])
	}

	jsonOK(w, map[string]any{"results": results})
}

// syncCaddy regenerates the full Caddyfile from the current database state and
// pushes it to the Caddy admin API for a zero-downtime reload.
func (s *Server) syncCaddy() error {
	// Network membership is desired state; re-apply it here because this runs
	// on every mutation. Caddy and the daemon have no deploy of their own, so
	// this is what makes their attachments self-healing.
	s.reconcileNetworkMembers()

	projects, err := s.store.ListProjects()
	if err != nil {
		return err
	}
	var routed []store.Project
	for _, p := range projects {
		// Paused projects are always routed — they get a 503 block even if
		// their container is stopped or static files are gone.
		if p.Paused {
			routed = append(routed, p)
			continue
		}
		if p.IsExternal() {
			// Poof does not own the upstream container, so its state is not
			// ours to gate on: publish the route and let Caddy report reality.
			routed = append(routed, p)
			continue
		}
		if p.IsStatic() {
			if s.static.IsDeployed(s.cfg.DataDir, p.Name) {
				routed = append(routed, p)
			}
		} else if s.container.IsRunning(p.Name) {
			routed = append(routed, p)
		}
	}
	redirects, err := s.store.ListRedirects()
	if err != nil {
		return err
	}
	snippets, err := s.store.GetAllCaddySnippets()
	if err != nil {
		return err
	}
	caddyfile := caddy.GenerateCaddyfile(routed, redirects, snippets, s.settingDomain(), s.cfg.PublicHost(), s.cfg.APIPort, s.cfg.CaddyStaticDir)
	return s.caddy.Reload(s.cfg.CaddyAdminURL, caddyfile)
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

// parseExternalTarget splits a `container:port` target. The port is optional
// and defaults to 80, matching the default for a normal project.
func parseExternalTarget(target string) (host string, port int, err error) {
	host, portStr, found := strings.Cut(target, ":")
	if host == "" {
		return "", 0, fmt.Errorf("external target must be <container> or <container>:<port>")
	}
	if !found {
		return host, defaults.Port, nil
	}
	port, convErr := strconv.Atoi(portStr)
	if convErr != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port %q in external target %q", portStr, target)
	}
	return host, port, nil
}

// externalRefusal explains why container lifecycle operations don't apply to
// an external project. Poof owns the domain and the routing for these, never
// the container — so deploying, rolling back or snapshotting is meaningless.
func externalRefusal(p *store.Project) string {
	return fmt.Sprintf(
		"%q is an external project: it routes to container %q, which Poof does not manage. "+
			"Deploy, rollback and snapshot only apply to containers Poof owns.",
		p.Name, p.External)
}
