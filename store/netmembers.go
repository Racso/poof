package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Network member kinds. A network's membership is desired state: Poof
// re-applies it, so an attachment survives container recreation — unlike a
// one-off `docker network connect`.
//
//   - project:   a Poof project; its container is attached on every (re)deploy
//   - container: a container Poof does not manage (Compose, hand-run)
//   - caddy:     the Caddy container, so it can route to members of this net
//   - poof:      the Poof daemon itself, for containers that call its API
//     internally instead of over the public URL
const (
	MemberProject   = "project"
	MemberContainer = "container"
	MemberCaddy     = "caddy"
	MemberPoof      = "poof"
)

// ValidMemberKind reports whether k is a recognized member kind.
func ValidMemberKind(k string) bool {
	switch k {
	case MemberProject, MemberContainer, MemberCaddy, MemberPoof:
		return true
	}
	return false
}

// NetworkMember is one attachment of something to a Poof-managed network.
// For the caddy and poof kinds, Member repeats the kind — there is only ever
// one of each.
type NetworkMember struct {
	ID        int64     `json:"id"`
	Network   string    `json:"network"`
	Member    string    `json:"member"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

// AddNetworkMember records an attachment. Idempotent: attaching something
// already attached succeeds without creating a duplicate.
func (s *Store) AddNetworkMember(network, member, kind string) (*NetworkMember, error) {
	if !ValidMemberKind(kind) {
		return nil, fmt.Errorf("invalid member kind %q", kind)
	}
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO network_members (network, member, kind) VALUES (?, ?, ?)`,
		network, member, kind,
	); err != nil {
		return nil, err
	}
	return s.GetNetworkMember(network, member, kind)
}

// GetNetworkMember returns a single attachment, or nil if absent.
func (s *Store) GetNetworkMember(network, member, kind string) (*NetworkMember, error) {
	var m NetworkMember
	err := s.db.QueryRow(
		`SELECT id, network, member, kind, created_at FROM network_members
		 WHERE network = ? AND member = ? AND kind = ?`, network, member, kind,
	).Scan(&m.ID, &m.Network, &m.Member, &m.Kind, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// RemoveNetworkMember detaches something from a network. Returns false if the
// attachment wasn't there.
func (s *Store) RemoveNetworkMember(network, member, kind string) (bool, error) {
	res, err := s.db.Exec(
		`DELETE FROM network_members WHERE network = ? AND member = ? AND kind = ?`,
		network, member, kind,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RemoveNetworkMemberByID detaches by row id (used by the legacy
// project-oriented API, which addresses attachments numerically).
func (s *Store) RemoveNetworkMemberByID(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM network_members WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListNetworkMembers returns everything attached to a network.
func (s *Store) ListNetworkMembers(network string) ([]NetworkMember, error) {
	return s.queryMembers(
		`SELECT id, network, member, kind, created_at FROM network_members
		 WHERE network = ? ORDER BY kind, member`, network)
}

// ListAllNetworkMembers returns every attachment across all networks — the
// input to reconciliation.
func (s *Store) ListAllNetworkMembers() ([]NetworkMember, error) {
	return s.queryMembers(
		`SELECT id, network, member, kind, created_at FROM network_members
		 ORDER BY network, kind, member`)
}

// ListNetworksForProject returns the networks a project is attached to. The
// deploy path uses this to re-apply membership on every (re)create.
func (s *Store) ListNetworksForProject(project string) ([]NetworkMember, error) {
	return s.queryMembers(
		`SELECT id, network, member, kind, created_at FROM network_members
		 WHERE kind = 'project' AND member = ? ORDER BY id`, project)
}

// CountNetworkMembers reports how many things are attached to a network.
// `poof net delete` refuses while this is non-zero.
func (s *Store) CountNetworkMembers(network string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM network_members WHERE network = ?`, network,
	).Scan(&n)
	return n, err
}

// DeleteNetworkMembersForProject removes a project's attachments. The old
// project_networks table did this via ON DELETE CASCADE; network_members has
// no foreign key (members may name containers Poof doesn't own), so project
// deletion cleans up explicitly.
func (s *Store) DeleteNetworkMembersForProject(project string) error {
	_, err := s.db.Exec(
		`DELETE FROM network_members WHERE kind = 'project' AND member = ?`, project)
	return err
}

// DeleteNetworkMembersForNetwork removes every attachment on a network.
func (s *Store) DeleteNetworkMembersForNetwork(network string) error {
	_, err := s.db.Exec(`DELETE FROM network_members WHERE network = ?`, network)
	return err
}

func (s *Store) queryMembers(query string, args ...interface{}) ([]NetworkMember, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NetworkMember
	for rows.Next() {
		var m NetworkMember
		if err := rows.Scan(&m.ID, &m.Network, &m.Member, &m.Kind, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetNetworkMemberByID returns an attachment by row id, or nil if absent.
// Used by the legacy project-oriented API, which addresses attachments
// numerically.
func (s *Store) GetNetworkMemberByID(id int64) (*NetworkMember, error) {
	var m NetworkMember
	err := s.db.QueryRow(
		`SELECT id, network, member, kind, created_at FROM network_members WHERE id = ?`, id,
	).Scan(&m.ID, &m.Network, &m.Member, &m.Kind, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}
