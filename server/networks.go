package server

import (
	"log"
	"net/url"

	"github.com/racso/poof/store"
)

// Per-app network isolation: every containerized project runs on its own
// Docker network, poof-app-<name>, with Caddy attached for routing. Networks
// are never shared implicitly — a container's only automatic neighbor is
// Caddy. Cross-project (or hand-managed) neighbors are always deliberate:
// either a Poof-managed extra network (`poof net`) or a manual
// `docker network connect` into a project's per-app net.
//
// poof-net itself is the control plane: Caddy and the Poof daemon only.

// appNetName is the per-project Docker network Poof manages for a project's
// container.
func appNetName(project string) string { return "poof-app-" + project }

// caddyContainerName derives Caddy's container name from the admin URL host
// (e.g. "http://caddy-proxy:2019" → "caddy-proxy"). It doubles as the
// container name on Docker networks.
func (s *Server) caddyContainerName() string {
	u, err := url.Parse(s.cfg.CaddyAdminURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// ensureProjectNetwork makes sure the project's per-app network exists and
// Caddy is attached to it. Idempotent — called on every (re)deploy. Returns
// the network name.
func (s *Server) ensureProjectNetwork(p *store.Project) (string, error) {
	net := appNetName(p.Name)
	if err := s.container.EnsureNetwork(net, false); err != nil {
		return "", err
	}
	if caddy := s.caddyContainerName(); caddy != "" {
		if err := s.container.ConnectNetwork(net, caddy); err != nil {
			return "", err
		}
	}
	return net, nil
}

// teardownAppNetwork detaches Caddy from the project's per-app network and
// removes it. Best-effort: a network kept alive by hand-attached containers
// is left in place with a warning.
func (s *Server) teardownAppNetwork(project string) {
	net := appNetName(project)
	if caddy := s.caddyContainerName(); caddy != "" {
		if err := s.container.DisconnectNetwork(net, caddy); err != nil {
			log.Printf("warning: detaching caddy from %s: %v", net, err)
		}
	}
	if err := s.container.RemoveNetwork(net); err != nil {
		log.Printf("warning: removing network %s: %v", net, err)
	}
}
