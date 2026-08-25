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

// reconcileNetworkMembers brings every Poof-managed network's actual Docker
// membership in line with the recorded desired state.
//
// This is what separates a Poof-managed attachment from a one-off
// `docker network connect`: for projects, membership is re-applied when the
// container is (re)created, but Caddy, the daemon and unmanaged containers
// have no deploy event of their own. Reconciling here means an attachment
// survives those containers being recreated out from under us — the failure
// mode being that Caddy silently loses a network and routing 502s with no
// obvious cause.
//
// Additive and idempotent: it attaches what's missing and never detaches, so
// a container someone wired up by hand is left alone. Best-effort by design —
// a single unreachable member must not block the rest.
func (s *Server) reconcileNetworkMembers() {
	members, err := s.store.ListAllNetworkMembers()
	if err != nil {
		log.Printf("warning: listing network members: %v", err)
		return
	}
	if len(members) == 0 {
		return
	}

	// Networks are declared in the store; make sure they exist before
	// attaching anything to them.
	ensured := map[string]bool{}
	for _, m := range members {
		if ensured[m.Network] {
			continue
		}
		internal := false
		if def, err := s.store.GetNetwork(m.Network); err == nil && def != nil {
			internal = def.Internal
		}
		if err := s.container.EnsureNetwork(m.Network, internal); err != nil {
			log.Printf("warning: ensuring network %s: %v", m.Network, err)
		}
		ensured[m.Network] = true
	}

	for _, m := range members {
		container, ok := s.resolveMemberContainer(m)
		if !ok {
			continue
		}
		// Skip members whose container doesn't exist yet (e.g. a project that
		// has never been deployed) — nothing to attach.
		if !s.container.ContainerExists(container) {
			continue
		}
		nets, err := s.container.ContainerNetworks(container)
		if err == nil && containsString(nets, m.Network) {
			continue
		}
		if err := s.container.ConnectNetwork(m.Network, container); err != nil {
			log.Printf("warning: attaching %s to %s: %v", container, m.Network, err)
			continue
		}
		log.Printf("network reconcile: attached %s to %s", container, m.Network)
	}
}

// resolveMemberContainer maps a stored member to the Docker container name it
// refers to. Returns false when the name can't be determined.
func (s *Server) resolveMemberContainer(m store.NetworkMember) (string, bool) {
	switch m.Kind {
	case store.MemberProject:
		return containerName(m.Member), true
	case store.MemberContainer:
		return m.Member, true
	case store.MemberCaddy:
		name := s.caddyContainerName()
		return name, name != ""
	case store.MemberPoof:
		name, err := s.container.SelfContainerName()
		if err != nil || name == "" {
			log.Printf("warning: cannot determine Poof's own container name: %v", err)
			return "", false
		}
		return name, true
	}
	return "", false
}

// containerName is the Docker container name Poof uses for a project.
func containerName(project string) string { return store.ContainerPrefix + project }

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
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
