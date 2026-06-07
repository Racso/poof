package server

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/racso/poof/docker"
	"github.com/racso/poof/store"
)

// appNetName is the per-project Docker network Poof manages for a project's
// container: its internal flag encodes egress, and Caddy is attached to it when
// the project has ingress. One net per project — never shared — so a container's
// only neighbor is ever Caddy (and only on ingress projects).
func appNetName(project string) string { return "poof-app-" + project }

// caddyContainerName derives Caddy's container name from the admin URL host
// (e.g. "http://caddy-proxy:2019" → "caddy-proxy"). It doubles as the container
// name on the Docker network.
func (s *Server) caddyContainerName() string {
	u, err := url.Parse(s.cfg.CaddyAdminURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func netKind(internal bool) string {
	if internal {
		return "internal"
	}
	return "external"
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// reconcileProjectNetworks brings a project's actual Docker network membership
// in line with its net mode. It is idempotent and (for the additive paths) safe
// on a running container — attaching a network needs no recreate. Returns the
// list of actions taken (or, with dryRun, that would be taken).
//
// Static projects have no container and are skipped. The egress axis is set at
// network-creation time via the internal flag; flipping it on an existing
// network (net-surgery) is handled by the live-update path, not here.
func (s *Server) reconcileProjectNetworks(p *store.Project, dryRun bool) ([]string, error) {
	if p.IsStatic() {
		return nil, nil
	}

	container := docker.ContainerName(p.Name)
	appNet := appNetName(p.Name)
	caddy := s.caddyContainerName()
	caddyNets, _ := s.container.ContainerNetworks(caddy)

	var actions []string
	sealed := p.NetMode == store.NetSealed

	if !sealed {
		// Ensure the per-app network exists with the right egress flag.
		internal := !p.HasEgress()
		if !s.container.NetworkExists(appNet) {
			actions = append(actions, fmt.Sprintf("create network %s (%s)", appNet, netKind(internal)))
			if !dryRun {
				if err := s.container.EnsureNetwork(appNet, internal); err != nil {
					return actions, fmt.Errorf("ensure %s: %w", appNet, err)
				}
			}
		}
		// Attach the container if it exists (skip if not yet deployed).
		if s.container.ContainerExists(container) {
			cnets, _ := s.container.ContainerNetworks(container)
			if !contains(cnets, appNet) {
				actions = append(actions, fmt.Sprintf("attach %s → %s", container, appNet))
				if !dryRun {
					if err := s.container.ConnectNetwork(appNet, container); err != nil {
						return actions, err
					}
				}
			}
		}
	} else {
		// Sealed: the project has no auto network. Detach the container from any
		// previously-provisioned per-app net (manual meshes are left untouched).
		if s.container.ContainerExists(container) {
			cnets, _ := s.container.ContainerNetworks(container)
			if contains(cnets, appNet) {
				actions = append(actions, fmt.Sprintf("detach %s ✕ %s", container, appNet))
				if !dryRun {
					if err := s.container.DisconnectNetwork(appNet, container); err != nil {
						return actions, err
					}
				}
			}
		}
	}

	// Caddy is attached to the per-app net iff the project has ingress.
	wantCaddy := !sealed && p.HasIngress()
	hasCaddy := contains(caddyNets, appNet)
	if wantCaddy && !hasCaddy {
		actions = append(actions, fmt.Sprintf("attach caddy → %s", appNet))
		if !dryRun {
			if err := s.container.ConnectNetwork(appNet, caddy); err != nil {
				return actions, err
			}
		}
	} else if !wantCaddy && hasCaddy {
		actions = append(actions, fmt.Sprintf("detach caddy ✕ %s", appNet))
		if !dryRun {
			if err := s.container.DisconnectNetwork(appNet, caddy); err != nil {
				return actions, err
			}
		}
	}

	return actions, nil
}

// projectNetReport is one project's reconciliation outcome.
type projectNetReport struct {
	Project string   `json:"project"`
	Actions []string `json:"actions"`
	Error   string   `json:"error,omitempty"`
}

// reconcileAllNetworks reconciles every project's network membership. Used by
// the migration sweep — additive and idempotent, safe to run repeatedly.
func (s *Server) reconcileAllNetworks(dryRun bool) ([]projectNetReport, error) {
	projects, err := s.store.ListProjects()
	if err != nil {
		return nil, err
	}
	reports := make([]projectNetReport, 0, len(projects))
	for i := range projects {
		p := projects[i]
		actions, err := s.reconcileProjectNetworks(&p, dryRun)
		rep := projectNetReport{Project: p.Name, Actions: actions}
		if err != nil {
			rep.Error = err.Error()
		}
		reports = append(reports, rep)
	}
	return reports, nil
}

// migrateNetworks is the POST /migrate/networks handler. With ?dry-run=true it
// reports the planned changes without applying them.
func (s *Server) migrateNetworks(w http.ResponseWriter, r *http.Request) {
	dryRun := strings.EqualFold(r.URL.Query().Get("dry-run"), "true")
	reports, err := s.reconcileAllNetworks(dryRun)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !dryRun {
		if err := s.syncCaddy(); err != nil {
			log.Printf("warning: caddy sync after network migration failed: %v", err)
		}
		log.Printf("network migration applied across %d project(s)", len(reports))
	}
	jsonOK(w, map[string]interface{}{"dry_run": dryRun, "projects": reports})
}
