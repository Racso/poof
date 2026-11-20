package server

import (
	"log"
	"net/http"

	"github.com/racso/poof/store"
	"github.com/racso/poof/workflow"
)

// CIAction is one change to make in a repository so a project's CI matches
// its registration: write the Poof-managed workflow file, or delete it. The
// server only computes these; the client performs them with a credential
// that already lives on the operator's machine (gh, a token) or prints a
// link for a browser. The server holds no GitHub credential at all.
type CIAction struct {
	Op      string `json:"op"`     // CIWrite or CIDelete
	Repo    string `json:"repo"`   // owner/name
	Branch  string `json:"branch"` // the project's branch: push triggers only fire for workflow files present on the pushed branch
	Path    string `json:"path"`   // relative to the repository root
	Content string `json:"content,omitempty"`
	Message string `json:"message"` // commit message
}

const (
	CIWrite  = "write"
	CIDelete = "delete"
)

// projectResponse is a project plus the CI actions its change calls for.
type projectResponse struct {
	store.Project
	CI []CIAction `json:"ci,omitempty"`
}

// ciWrite is the action that puts p's workflow in place.
func (s *Server) ciWrite(p *store.Project) CIAction {
	owner, name := s.splitRepo(p.Repo)
	return CIAction{
		Op:     CIWrite,
		Repo:   owner + "/" + name,
		Branch: p.Branch,
		Path:   workflow.File(p.Name),
		Content: workflow.Render(workflow.Spec{
			ProjectName: p.Name,
			PoofURL:     s.cfg.PublicURL,
			Branch:      p.Branch,
			Image:       p.Image,
			Folder:      p.Folder,
			Static:      p.Static,
			CIMode:      p.CIMode,
			Build:       p.Build,
		}),
		Message: "chore: update Poof! deploy workflow",
	}
}

// ciDelete is the action that removes projectName's workflow from branch of
// repo. Deleting a file that is not there is a no-op for the client.
func (s *Server) ciDelete(repo, branch, projectName string) CIAction {
	owner, name := s.splitRepo(repo)
	return CIAction{
		Op:      CIDelete,
		Repo:    owner + "/" + name,
		Branch:  branch,
		Path:    workflow.File(projectName),
		Message: "chore: remove Poof! deploy workflow",
	}
}

// ciPlan returns what the repository should receive so it matches p: the
// workflow when CI is on, its removal when CI is off. External projects have
// no repository and get nothing.
func (s *Server) ciPlan(p *store.Project) []CIAction {
	if p.IsExternal() || p.Repo == "" {
		return nil
	}
	if p.CI {
		return []CIAction{s.ciWrite(p)}
	}
	return []CIAction{s.ciDelete(p.Repo, p.Branch, p.Name)}
}

// getProjectCI returns the CI plan for a project without changing anything:
// what `poof ci print` shows and `poof refresh` applies.
func (s *Server) getProjectCI(w http.ResponseWriter, r *http.Request) {
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
	jsonOK(w, map[string]interface{}{"ci": s.ciPlan(p), "ci_mode": p.CIMode})
}

// legacyGitHubSettings are the server-side GitHub credentials of earlier
// releases (a PAT, the sealed device-flow login, the App client id). The
// server no longer uses any of them, so a database carrying one is purged
// at startup: a credential nothing reads is still a credential to steal.
var legacyGitHubSettings = []string{"github-token", "github-login", "github-client-id"}

func (s *Server) purgeGitHubCredentials() {
	for _, key := range legacyGitHubSettings {
		if v, _ := s.store.GetSetting(key); v == "" {
			continue
		}
		if err := s.store.DeleteSetting(key); err != nil {
			log.Printf("warning: could not delete stale setting %s: %v", key, err)
			continue
		}
		log.Printf("deleted stale setting %s: the server no longer holds GitHub credentials (see 'poof ci --help')", key)
	}
}
