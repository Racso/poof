package store_test

import (
	"os"
	"testing"
	"time"

	"github.com/racso/poof/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	f, err := os.CreateTemp("", "poof-test-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	st, err := store.Open(f.Name())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func sampleProject(name string) store.Project {
	return store.Project{
		Name:   name,
		Domain: name + ".rac.so",
		Image:  "ghcr.io/racso/" + name,
		Repo:   "racso/" + name,
		Branch: "main",
		Port:   80,
	}
}

// --- Project CRUD ---

func TestCreateAndGetProject(t *testing.T) {
	st := newTestStore(t)
	p := sampleProject("demo")

	if err := st.CreateProject(p); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetProject("demo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected project, got nil")
	}
	if got.Name != p.Name {
		t.Errorf("name: got %q, want %q", got.Name, p.Name)
	}
	if got.Domain != p.Domain {
		t.Errorf("domain: got %q, want %q", got.Domain, p.Domain)
	}
	if got.Repo != p.Repo {
		t.Errorf("repo: got %q, want %q", got.Repo, p.Repo)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	st := newTestStore(t)
	got, err := st.GetProject("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestListProjects(t *testing.T) {
	st := newTestStore(t)

	for _, name := range []string{"beta", "alpha", "gamma"} {
		if err := st.CreateProject(sampleProject(name)); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	projects, err := st.ListProjects()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(projects))
	}
	// Should be sorted alphabetically.
	if projects[0].Name != "alpha" || projects[1].Name != "beta" || projects[2].Name != "gamma" {
		t.Errorf("unexpected order: %v", projects)
	}
}

func TestDeleteProject(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateProject(sampleProject("demo")); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := st.DeleteProject("demo"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, err := st.GetProject("demo")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestDuplicateProjectName(t *testing.T) {
	st := newTestStore(t)
	p := sampleProject("demo")
	if err := st.CreateProject(p); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := st.CreateProject(p); err == nil {
		t.Error("expected error on duplicate, got nil")
	}
}

// --- Deployments ---

func TestRecordAndRetrieveDeployment(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateProject(sampleProject("demo")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	id, err := st.RecordDeployment("demo", "ghcr.io/racso/demo:abc123", "running")
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero deployment ID")
	}

	if err := st.UpdateDeploymentStatus(id, "success"); err != nil {
		t.Fatalf("update status: %v", err)
	}

	last, err := st.LastDeployment("demo")
	if err != nil {
		t.Fatalf("last deployment: %v", err)
	}
	if last == nil {
		t.Fatal("expected deployment, got nil")
	}
	if last.Image != "ghcr.io/racso/demo:abc123" {
		t.Errorf("image: got %q", last.Image)
	}
	if last.Status != "success" {
		t.Errorf("status: got %q, want success", last.Status)
	}
}

func TestLastDeploymentNone(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateProject(sampleProject("demo")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	last, err := st.LastDeployment("demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if last != nil {
		t.Errorf("expected nil, got %+v", last)
	}
}

func TestPreviousDeploymentForRollback(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateProject(sampleProject("demo")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	images := []string{"img:v1", "img:v2", "img:v3"}
	for _, img := range images {
		id, _ := st.RecordDeployment("demo", img, "running")
		st.UpdateDeploymentStatus(id, "success")
		// Small sleep so timestamps differ.
		time.Sleep(2 * time.Millisecond)
	}

	prev, err := st.PreviousDeployment("demo")
	if err != nil {
		t.Fatalf("previous deployment: %v", err)
	}
	if prev == nil {
		t.Fatal("expected previous deployment, got nil")
	}
	if prev.Image != "img:v2" {
		t.Errorf("expected img:v2 (second-to-last), got %q", prev.Image)
	}
}

func TestPreviousDeploymentSkipsFailures(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateProject(sampleProject("demo")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// v1 = success, v2 = failed, v3 = success (current)
	id1, _ := st.RecordDeployment("demo", "img:v1", "running")
	st.UpdateDeploymentStatus(id1, "success")
	time.Sleep(2 * time.Millisecond)

	id2, _ := st.RecordDeployment("demo", "img:v2", "running")
	st.UpdateDeploymentStatus(id2, "failed")
	time.Sleep(2 * time.Millisecond)

	id3, _ := st.RecordDeployment("demo", "img:v3", "running")
	st.UpdateDeploymentStatus(id3, "success")

	prev, err := st.PreviousDeployment("demo")
	if err != nil {
		t.Fatalf("previous deployment: %v", err)
	}
	if prev == nil {
		t.Fatal("expected previous deployment, got nil")
	}
	// Should skip v2 (failed) and land on v1.
	if prev.Image != "img:v1" {
		t.Errorf("expected img:v1 (last success before current), got %q", prev.Image)
	}
}

func TestListDeployments(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateProject(sampleProject("demo")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	for i := 0; i < 5; i++ {
		st.RecordDeployment("demo", "img:v"+string(rune('0'+i)), "success")
		time.Sleep(2 * time.Millisecond)
	}

	deps, err := st.ListDeployments("demo", 3)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(deps) != 3 {
		t.Errorf("expected 3, got %d", len(deps))
	}
}

func TestDeploymentsCascadeOnDelete(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateProject(sampleProject("demo")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	st.RecordDeployment("demo", "img:v1", "success")

	if err := st.DeleteProject("demo"); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	// Deployments should be gone too (CASCADE).
	deps, err := st.ListDeployments("demo", 10)
	if err != nil {
		t.Fatalf("list deployments after delete: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("expected 0 deployments after project delete, got %d", len(deps))
	}
}

// --- Env Vars ---

func TestSetAndGetEnvVars(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateProject(sampleProject("demo")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	if err := st.SetEnvVar("demo", "DB_URL", "postgres://localhost/demo"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := st.SetEnvVar("demo", "SECRET", "hunter2"); err != nil {
		t.Fatalf("set: %v", err)
	}

	vars, err := st.GetEnvVars("demo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(vars) != 2 {
		t.Fatalf("expected 2 vars, got %d", len(vars))
	}
	if vars["DB_URL"] != "postgres://localhost/demo" {
		t.Errorf("DB_URL: got %q", vars["DB_URL"])
	}
	if vars["SECRET"] != "hunter2" {
		t.Errorf("SECRET: got %q", vars["SECRET"])
	}
}

func TestSetEnvVarOverwrite(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateProject(sampleProject("demo")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	st.SetEnvVar("demo", "KEY", "original")
	st.SetEnvVar("demo", "KEY", "updated")

	vars, _ := st.GetEnvVars("demo")
	if vars["KEY"] != "updated" {
		t.Errorf("expected updated, got %q", vars["KEY"])
	}
	if len(vars) != 1 {
		t.Errorf("expected 1 var, got %d (upsert should not duplicate)", len(vars))
	}
}

func TestUnsetEnvVar(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateProject(sampleProject("demo")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	st.SetEnvVar("demo", "A", "1")
	st.SetEnvVar("demo", "B", "2")
	st.UnsetEnvVar("demo", "A")

	vars, _ := st.GetEnvVars("demo")
	if _, ok := vars["A"]; ok {
		t.Error("A should have been removed")
	}
	if vars["B"] != "2" {
		t.Error("B should still be present")
	}
}

func TestEnvVarsCascadeOnDelete(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateProject(sampleProject("demo")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	st.SetEnvVar("demo", "KEY", "value")
	st.DeleteProject("demo")

	vars, err := st.GetEnvVars("demo")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if len(vars) != 0 {
		t.Errorf("expected 0 env vars after project delete, got %d", len(vars))
	}
}

// --- Caddy Snippets ---

func TestSetAndGetCaddySnippet(t *testing.T) {
	st := newTestStore(t)
	st.CreateProject(sampleProject("demo"))

	if err := st.SetCaddySnippet("demo", "redir /install https://example.com 302"); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := st.GetCaddySnippet("demo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "redir /install https://example.com 302" {
		t.Errorf("got %q", got)
	}
}

func TestGetCaddySnippetEmpty(t *testing.T) {
	st := newTestStore(t)
	st.CreateProject(sampleProject("demo"))

	got, err := st.GetCaddySnippet("demo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string for nonexistent snippet, got %q", got)
	}
}

func TestSetCaddySnippetOverwrite(t *testing.T) {
	st := newTestStore(t)
	st.CreateProject(sampleProject("demo"))

	st.SetCaddySnippet("demo", "v1")
	st.SetCaddySnippet("demo", "v2")

	got, _ := st.GetCaddySnippet("demo")
	if got != "v2" {
		t.Errorf("expected v2, got %q", got)
	}
}

func TestDeleteCaddySnippet(t *testing.T) {
	st := newTestStore(t)
	st.CreateProject(sampleProject("demo"))
	st.SetCaddySnippet("demo", "content")

	deleted, err := st.DeleteCaddySnippet("demo")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Error("expected deleted=true")
	}

	got, _ := st.GetCaddySnippet("demo")
	if got != "" {
		t.Errorf("expected empty after delete, got %q", got)
	}
}

func TestDeleteCaddySnippetNotFound(t *testing.T) {
	st := newTestStore(t)
	st.CreateProject(sampleProject("demo"))

	deleted, err := st.DeleteCaddySnippet("demo")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted {
		t.Error("expected deleted=false when no snippet exists")
	}
}

func TestCaddySnippetCascadeOnDelete(t *testing.T) {
	st := newTestStore(t)
	st.CreateProject(sampleProject("demo"))
	st.SetCaddySnippet("demo", "content")

	st.DeleteProject("demo")

	got, err := st.GetCaddySnippet("demo")
	if err != nil {
		t.Fatalf("get after project delete: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty after cascade delete, got %q", got)
	}
}

func TestGetAllCaddySnippets(t *testing.T) {
	st := newTestStore(t)
	st.CreateProject(sampleProject("alpha"))
	st.CreateProject(sampleProject("beta"))

	st.SetCaddySnippet("alpha", "snippet-a")
	st.SetCaddySnippet("beta", "snippet-b")

	all, err := st.GetAllCaddySnippets()
	if err != nil {
		t.Fatalf("get all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
	if all["alpha"] != "snippet-a" {
		t.Errorf("alpha: got %q", all["alpha"])
	}
	if all["beta"] != "snippet-b" {
		t.Errorf("beta: got %q", all["beta"])
	}
}

// --- GC Policies ---

func intPtr(v int) *int { return &v }

func TestSetAndGetGCPolicy(t *testing.T) {
	st := newTestStore(t)

	if err := st.SetGCPolicy(store.GCPolicy{Project: "demo", KeepCount: intPtr(5)}); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := st.GetGCPolicy("demo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.KeepCount == nil || *got.KeepCount != 5 {
		t.Fatalf("got %+v, want keep=5", got)
	}
	if got.OlderThanDays != nil {
		t.Errorf("older_than: got %v, want nil", *got.OlderThanDays)
	}
	if got.Disabled {
		t.Errorf("disabled: want false")
	}
}

func TestGetGCPolicyNotFound(t *testing.T) {
	st := newTestStore(t)
	got, err := st.GetGCPolicy("missing")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestSetGCPolicyOverwrites(t *testing.T) {
	st := newTestStore(t)
	st.SetGCPolicy(store.GCPolicy{Project: "demo", KeepCount: intPtr(5)})
	st.SetGCPolicy(store.GCPolicy{Project: "demo", OlderThanDays: intPtr(14)})

	got, _ := st.GetGCPolicy("demo")
	if got.KeepCount != nil {
		t.Errorf("keep_count: got %v, want nil after overwrite", *got.KeepCount)
	}
	if got.OlderThanDays == nil || *got.OlderThanDays != 14 {
		t.Errorf("older_than: got %v, want 14", got.OlderThanDays)
	}
}

func TestDeleteGCPolicy(t *testing.T) {
	st := newTestStore(t)
	st.SetGCPolicy(store.GCPolicy{Project: "demo", KeepCount: intPtr(5)})
	if err := st.DeleteGCPolicy("demo"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := st.GetGCPolicy("demo")
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestListGCPolicies(t *testing.T) {
	st := newTestStore(t)
	st.SetGCPolicy(store.GCPolicy{Project: store.GCPolicyGlobalKey, KeepCount: intPtr(3)})
	st.SetGCPolicy(store.GCPolicy{Project: "alpha", KeepCount: intPtr(5)})
	st.SetGCPolicy(store.GCPolicy{Project: "beta", Disabled: true})

	all, err := st.ListGCPolicies()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(all))
	}
}

func TestResolveGCPolicy_Default(t *testing.T) {
	st := newTestStore(t)
	pol, enabled := st.ResolveGCPolicy("demo")
	if !enabled {
		t.Fatal("expected enabled with built-in default")
	}
	if pol.KeepCount == nil || *pol.KeepCount != 3 {
		t.Errorf("default keep: got %v, want 3", pol.KeepCount)
	}
	if pol.OlderThanDays != nil {
		t.Errorf("default older_than: got %v, want nil", pol.OlderThanDays)
	}
}

func TestResolveGCPolicy_GlobalOverridesDefault(t *testing.T) {
	st := newTestStore(t)
	st.SetGCPolicy(store.GCPolicy{Project: store.GCPolicyGlobalKey, KeepCount: intPtr(7)})

	pol, enabled := st.ResolveGCPolicy("demo")
	if !enabled {
		t.Fatal("expected enabled")
	}
	if *pol.KeepCount != 7 {
		t.Errorf("got keep=%d, want 7", *pol.KeepCount)
	}
	if pol.Project != "demo" {
		t.Errorf("project name not rebound: got %q", pol.Project)
	}
}

func TestResolveGCPolicy_ProjectOverridesGlobal(t *testing.T) {
	st := newTestStore(t)
	st.SetGCPolicy(store.GCPolicy{Project: store.GCPolicyGlobalKey, KeepCount: intPtr(7)})
	st.SetGCPolicy(store.GCPolicy{Project: "demo", KeepCount: intPtr(2)})

	pol, _ := st.ResolveGCPolicy("demo")
	if *pol.KeepCount != 2 {
		t.Errorf("got keep=%d, want 2", *pol.KeepCount)
	}
}

func TestResolveGCPolicy_ProjectDisabledOverridesGlobal(t *testing.T) {
	st := newTestStore(t)
	st.SetGCPolicy(store.GCPolicy{Project: store.GCPolicyGlobalKey, KeepCount: intPtr(7)})
	st.SetGCPolicy(store.GCPolicy{Project: "demo", Disabled: true})

	if _, enabled := st.ResolveGCPolicy("demo"); enabled {
		t.Error("expected disabled for project")
	}
	if _, enabled := st.ResolveGCPolicy("other"); !enabled {
		t.Error("expected enabled for other project (still inherits global)")
	}
}

func TestResolveGCPolicy_GlobalDisabledSkipsDefault(t *testing.T) {
	st := newTestStore(t)
	st.SetGCPolicy(store.GCPolicy{Project: store.GCPolicyGlobalKey, Disabled: true})

	if _, enabled := st.ResolveGCPolicy("demo"); enabled {
		t.Error("expected disabled — global off should suppress built-in default")
	}
}
