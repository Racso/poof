package store_test

import (
	"os"
	"testing"

	"github.com/racso/poof/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	f, err := os.CreateTemp("", "poof-netmem-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	st, err := store.Open(f.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestNetworkMembersCRUD(t *testing.T) {
	st := newStore(t)
	st.CreateNetwork(store.Network{Name: "mesh"})

	// A project, an unmanaged container, Caddy, and the daemon can all coexist
	// on one network — the whole point of the new table.
	for _, m := range []struct{ member, kind string }{
		{"api", store.MemberProject},
		{"legacy-worker", store.MemberContainer},
		{"caddy", store.MemberCaddy},
		{"poof", store.MemberPoof},
	} {
		if _, err := st.AddNetworkMember("mesh", m.member, m.kind); err != nil {
			t.Fatalf("add %s/%s: %v", m.kind, m.member, err)
		}
	}

	members, err := st.ListNetworkMembers("mesh")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(members) != 4 {
		t.Fatalf("expected 4 members, got %d", len(members))
	}

	// Idempotent: re-adding does not duplicate.
	if _, err := st.AddNetworkMember("mesh", "api", store.MemberProject); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if n, _ := st.CountNetworkMembers("mesh"); n != 4 {
		t.Errorf("after re-add, count = %d, want 4", n)
	}

	// Same name under a different kind is a distinct membership.
	if _, err := st.AddNetworkMember("mesh", "api", store.MemberContainer); err != nil {
		t.Fatalf("add same name other kind: %v", err)
	}
	if n, _ := st.CountNetworkMembers("mesh"); n != 5 {
		t.Errorf("count = %d, want 5", n)
	}

	removed, err := st.RemoveNetworkMember("mesh", "api", store.MemberContainer)
	if err != nil || !removed {
		t.Errorf("remove = %v, %v; want true, nil", removed, err)
	}
	if removed, _ := st.RemoveNetworkMember("mesh", "nope", store.MemberProject); removed {
		t.Error("removing an absent member should report false")
	}
}

func TestListNetworksForProject(t *testing.T) {
	st := newStore(t)
	st.CreateNetwork(store.Network{Name: "a"})
	st.CreateNetwork(store.Network{Name: "b"})
	st.AddNetworkMember("a", "api", store.MemberProject)
	st.AddNetworkMember("b", "api", store.MemberProject)
	st.AddNetworkMember("a", "other", store.MemberProject)
	// Non-project members must not leak into a project's deploy-time list.
	st.AddNetworkMember("a", "caddy", store.MemberCaddy)

	nets, err := st.ListNetworksForProject("api")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(nets) != 2 {
		t.Fatalf("expected 2 networks for api, got %d", len(nets))
	}
	for _, n := range nets {
		if n.Kind != store.MemberProject || n.Member != "api" {
			t.Errorf("unexpected row: %+v", n)
		}
	}
}

func TestDeleteNetworkMembersForProject(t *testing.T) {
	st := newStore(t)
	st.CreateNetwork(store.Network{Name: "a"})
	st.AddNetworkMember("a", "api", store.MemberProject)
	st.AddNetworkMember("a", "api", store.MemberContainer) // different kind, must survive
	st.AddNetworkMember("a", "caddy", store.MemberCaddy)

	if err := st.DeleteNetworkMembersForProject("api"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	members, _ := st.ListNetworkMembers("a")
	if len(members) != 2 {
		t.Fatalf("expected 2 remaining, got %d (%+v)", len(members), members)
	}
	for _, m := range members {
		if m.Kind == store.MemberProject {
			t.Errorf("project membership should have been deleted: %+v", m)
		}
	}
}

func TestInvalidMemberKindRejected(t *testing.T) {
	st := newStore(t)
	st.CreateNetwork(store.Network{Name: "a"})
	if _, err := st.AddNetworkMember("a", "x", "nonsense"); err == nil {
		t.Error("expected an error for an invalid member kind")
	}
}
