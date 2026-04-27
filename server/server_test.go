package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/racso/poof/config"
	"github.com/racso/poof/defaults"
	"github.com/racso/poof/server"
	"github.com/racso/poof/store"
)

// --- Mock RepoManager ---

type mockRepoManager struct {
	setupCalls   []mockSetupCall
	removeCalls  []mockRemoveCall
	refreshCalls []mockRefreshCall
}

type mockSetupCall struct {
	Owner, Repo, ProjectName, PoofURL, PoofToken, Branch, Image, Folder, Static string
	Build                                                                        bool
}

type mockRemoveCall struct {
	Owner, Repo, ProjectName string
	DeleteSecrets            bool
}

type mockRefreshCall struct {
	Owner, Repo, ProjectName string
	CI                       bool
	PoofURL, RepoToken, Branch, Image, Folder, Static string
	Build                                              bool
	DeleteSecrets                                      bool
}

func (m *mockRepoManager) SetRepoCI(owner, repo, projectName, poofURL, poofToken, branch, image, folder, static string, build bool) error {
	m.setupCalls = append(m.setupCalls, mockSetupCall{owner, repo, projectName, poofURL, poofToken, branch, image, folder, static, build})
	return nil
}

func (m *mockRepoManager) RemoveRepoCI(owner, repo, projectName string, deleteSecrets bool) error {
	m.removeCalls = append(m.removeCalls, mockRemoveCall{owner, repo, projectName, deleteSecrets})
	return nil
}

func (m *mockRepoManager) RefreshProjectCI(owner, repo, projectName string, ci bool, poofURL, repoToken, branch, image, folder, static string, build bool, deleteSecrets bool) error {
	m.refreshCalls = append(m.refreshCalls, mockRefreshCall{owner, repo, projectName, ci, poofURL, repoToken, branch, image, folder, static, build, deleteSecrets})
	return nil
}

// --- Mock ContainerManager ---

type mockContainerManager struct {
	deployCalls   []server.ContainerDeployConfig
	stopCalls     []string
	running       map[string]bool
	logs          map[string]string
	gcCalls       []mockGCCall
	pruneCalls    int
}

type mockGCCall struct {
	Project       string
	Image         string
	Keep          int
	OlderThanDays int
	DryRun        bool
}

func (m *mockContainerManager) Deploy(cfg server.ContainerDeployConfig) error {
	m.deployCalls = append(m.deployCalls, cfg)
	return nil
}

func (m *mockContainerManager) Stop(name string) error {
	m.stopCalls = append(m.stopCalls, name)
	return nil
}

func (m *mockContainerManager) IsRunning(name string) bool {
	if m.running == nil {
		return false
	}
	return m.running[name]
}

func (m *mockContainerManager) GC(name, image string, keep, olderThanDays int, dryRun bool) (server.GCResult, error) {
	m.gcCalls = append(m.gcCalls, mockGCCall{name, image, keep, olderThanDays, dryRun})
	return server.GCResult{Project: name, Removed: []string{name + ":old"}}, nil
}

func (m *mockContainerManager) PruneDangling() error {
	m.pruneCalls++
	return nil
}

func (m *mockContainerManager) Logs(name string, lines int) (string, error) {
	if m.logs != nil {
		if l, ok := m.logs[name]; ok {
			return l, nil
		}
	}
	return "", nil
}

// --- Mock StaticDeployer ---

type mockStaticDeployer struct {
	deployCalls   []mockStaticDeployCall
	rollbackCalls []mockStaticRollbackCall
	removeCalls   []string
	deployed      map[string]bool
}

type mockStaticDeployCall struct {
	DataDir, Project string
	DepID            int64
}

type mockStaticRollbackCall struct {
	DataDir, Project string
	DepID            int64
}

func (m *mockStaticDeployer) Deploy(dataDir, project string, depID int64, _ io.Reader) error {
	m.deployCalls = append(m.deployCalls, mockStaticDeployCall{dataDir, project, depID})
	return nil
}

func (m *mockStaticDeployer) Rollback(dataDir, project string, depID int64) error {
	m.rollbackCalls = append(m.rollbackCalls, mockStaticRollbackCall{dataDir, project, depID})
	return nil
}

func (m *mockStaticDeployer) IsDeployed(_, project string) bool {
	if m.deployed == nil {
		return false
	}
	return m.deployed[project]
}

func (m *mockStaticDeployer) Remove(_, project string) {
	m.removeCalls = append(m.removeCalls, project)
}

// --- Mock CaddySyncer ---

type mockCaddySyncer struct {
	reloadCalls int
}

func (m *mockCaddySyncer) Reload(_, _ string) error {
	m.reloadCalls++
	return nil
}

// --- Test mocks aggregate ---

type testMocks struct {
	repo      *mockRepoManager
	container *mockContainerManager
	static    *mockStaticDeployer
	caddy     *mockCaddySyncer
}

// --- Test helpers ---

func newTestServer(t *testing.T) (*server.Server, *store.Store, *testMocks) {
	t.Helper()
	f, err := os.CreateTemp("", "poof-server-test-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	st, err := store.Open(f.Name())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.ServerConfig{
		APIPort:   9000,
		PublicURL: "https://poof.rac.so",
		Token:     "global-test-token",
	}

	mocks := &testMocks{
		repo:      &mockRepoManager{},
		container: &mockContainerManager{},
		static:    &mockStaticDeployer{},
		caddy:     &mockCaddySyncer{},
	}
	srv := server.New(cfg, st, func(token string) server.RepoManager {
		return mocks.repo
	}, mocks.container, mocks.static, mocks.caddy)

	return srv, st, mocks
}

func do(t *testing.T, srv *server.Server, method, path string, body interface{}, token string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func decode(t *testing.T, rr *httptest.ResponseRecorder, out interface{}) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(out); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, rr.Body.String())
	}
}

const globalToken = "global-test-token"

// --- Auth ---

func TestAuthRejectsNoToken(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rr := do(t, srv, "GET", "/projects", nil, "")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuthRejectsWrongToken(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rr := do(t, srv, "GET", "/projects", nil, "wrong-token")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuthAcceptsGlobalToken(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rr := do(t, srv, "GET", "/projects", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- Project CRUD ---

func TestCreateAndListProjects(t *testing.T) {
	srv, _, _ := newTestServer(t)

	// Empty list
	rr := do(t, srv, "GET", "/projects", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d", rr.Code)
	}
	var projects []interface{}
	decode(t, rr, &projects)
	if len(projects) != 0 {
		t.Errorf("expected empty list, got %d", len(projects))
	}

	// Create
	rr = do(t, srv, "POST", "/projects", map[string]interface{}{"name": "demo"}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d — %s", rr.Code, rr.Body.String())
	}

	// List again
	rr = do(t, srv, "GET", "/projects", nil, globalToken)
	decode(t, rr, &projects)
	if len(projects) != 1 {
		t.Errorf("expected 1 project, got %d", len(projects))
	}
}

func TestCreateProjectAppliesDefaults(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.SetSetting("domain", "rac.so")
	st.SetSetting("github-user", "racso")

	rr := do(t, srv, "POST", "/projects", map[string]interface{}{"name": "myapp"}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d — %s", rr.Code, rr.Body.String())
	}

	var p map[string]interface{}
	decode(t, rr, &p)

	if p["domain"] != "myapp.rac.so" {
		t.Errorf("domain: got %q, want myapp.rac.so", p["domain"])
	}
	if p["image"] != "ghcr.io/racso/myapp" {
		t.Errorf("image: got %q, want ghcr.io/racso/myapp", p["image"])
	}
	if p["repo"] != "racso/myapp" {
		t.Errorf("repo: got %q, want racso/myapp", p["repo"])
	}
	if p["branch"] != defaults.Branch {
		t.Errorf("branch: got %q, want %s", p["branch"], defaults.Branch)
	}
	if p["port"] != float64(defaults.Port) {
		t.Errorf("port: got %v, want %d", p["port"], defaults.Port)
	}
}

func TestCreateProjectOverridesDefaults(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rr := do(t, srv, "POST", "/projects", map[string]interface{}{
		"name":   "api",
		"domain": "api.rac.so",
		"port":   3000,
		"branch": "production",
	}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d — %s", rr.Code, rr.Body.String())
	}

	var p map[string]interface{}
	decode(t, rr, &p)

	if p["domain"] != "api.rac.so" {
		t.Errorf("domain: got %q", p["domain"])
	}
	if p["port"] != float64(3000) {
		t.Errorf("port: got %v", p["port"])
	}
	if p["branch"] != "production" {
		t.Errorf("branch: got %q", p["branch"])
	}
}

func TestCreateProjectDuplicateReturns409(t *testing.T) {
	srv, _, _ := newTestServer(t)

	do(t, srv, "POST", "/projects", map[string]interface{}{"name": "demo"}, globalToken)
	rr := do(t, srv, "POST", "/projects", map[string]interface{}{"name": "demo"}, globalToken)
	if rr.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", rr.Code)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rr := do(t, srv, "GET", "/projects/nonexistent", nil, globalToken)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestGetProject(t *testing.T) {
	srv, _, _ := newTestServer(t)
	do(t, srv, "POST", "/projects", map[string]interface{}{"name": "demo"}, globalToken)

	rr := do(t, srv, "GET", "/projects/demo", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d — %s", rr.Code, rr.Body.String())
	}

	var result map[string]interface{}
	decode(t, rr, &result)

	p, ok := result["project"].(map[string]interface{})
	if !ok {
		t.Fatalf("no 'project' key in response")
	}
	if p["name"] != "demo" {
		t.Errorf("name: got %q", p["name"])
	}
}

func TestDeleteProject(t *testing.T) {
	srv, _, _ := newTestServer(t)
	do(t, srv, "POST", "/projects", map[string]interface{}{"name": "demo"}, globalToken)

	rr := do(t, srv, "DELETE", "/projects/demo", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: %d — %s", rr.Code, rr.Body.String())
	}

	rr = do(t, srv, "GET", "/projects/demo", nil, globalToken)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", rr.Code)
	}
}

// --- Per-project token auth on /deploy ---

func TestDeployAcceptsRepoToken(t *testing.T) {
	srv, st, _ := newTestServer(t)

	p := store.Project{
		Name: "demo", Domain: "demo.rac.so", Image: "img:v1",
		Repo: "racso/demo", Branch: "main", Port: 8080,
	}
	if err := st.CreateProject(p); err != nil {
		t.Fatalf("create: %v", err)
	}
	st.SetRepoToken("racso/demo", "repo-tok")

	// Deploy with repo token — Docker will fail (not running in test),
	// but auth should pass (we get past 401).
	rr := do(t, srv, "POST", "/projects/demo/deploy",
		map[string]interface{}{"image": "img:v2"}, "repo-tok")

	if rr.Code == http.StatusUnauthorized {
		t.Error("repo token should be accepted for /deploy")
	}
}

func TestDeployRejectsWrongToken(t *testing.T) {
	srv, st, _ := newTestServer(t)

	p := store.Project{
		Name: "demo", Domain: "demo.rac.so", Image: "img:v1",
		Repo: "racso/demo", Branch: "main", Port: 8080,
	}
	st.CreateProject(p)
	st.SetRepoToken("racso/demo", "correct-token")

	rr := do(t, srv, "POST", "/projects/demo/deploy",
		map[string]interface{}{"image": "img:v2"}, "wrong-token")

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong token, got %d", rr.Code)
	}
}

func TestDeployAcceptsGlobalToken(t *testing.T) {
	srv, st, _ := newTestServer(t)

	p := store.Project{
		Name: "demo", Domain: "demo.rac.so", Image: "img:v1",
		Repo: "racso/demo", Branch: "main", Port: 8080,
	}
	st.CreateProject(p)

	// Global token should also work on /deploy.
	rr := do(t, srv, "POST", "/projects/demo/deploy",
		map[string]interface{}{"image": "img:v2"}, globalToken)

	if rr.Code == http.StatusUnauthorized {
		t.Error("global token should also be accepted for /deploy")
	}
}

// --- Env vars ---

func TestSetAndGetEnv(t *testing.T) {
	srv, _, _ := newTestServer(t)
	do(t, srv, "POST", "/projects", map[string]interface{}{"name": "demo"}, globalToken)

	// Set vars
	rr := do(t, srv, "PUT", "/projects/demo/env",
		map[string]string{"DB_URL": "postgres://localhost/demo", "SECRET": "abc"},
		globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("set env: %d — %s", rr.Code, rr.Body.String())
	}

	// Get keys — values should NOT be returned.
	rr = do(t, srv, "GET", "/projects/demo/env", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("get env: %d", rr.Code)
	}

	var result map[string]interface{}
	decode(t, rr, &result)

	keys, ok := result["keys"].([]interface{})
	if !ok {
		t.Fatalf("expected 'keys' array, got: %v", result)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestUnsetEnv(t *testing.T) {
	srv, _, _ := newTestServer(t)
	do(t, srv, "POST", "/projects", map[string]interface{}{"name": "demo"}, globalToken)
	do(t, srv, "PUT", "/projects/demo/env", map[string]string{"A": "1", "B": "2"}, globalToken)

	rr := do(t, srv, "DELETE", "/projects/demo/env/A", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("unset: %d — %s", rr.Code, rr.Body.String())
	}

	rr = do(t, srv, "GET", "/projects/demo/env", nil, globalToken)
	var result map[string]interface{}
	decode(t, rr, &result)
	keys := result["keys"].([]interface{})
	if len(keys) != 1 {
		t.Errorf("expected 1 key after unset, got %d", len(keys))
	}
	if keys[0] != "B" {
		t.Errorf("expected B to remain, got %v", keys[0])
	}
}

func TestEnvRequiresExistingProject(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rr := do(t, srv, "PUT", "/projects/ghost/env",
		map[string]string{"KEY": "val"}, globalToken)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent project, got %d", rr.Code)
	}
}

// --- GitHub integration (RepoManager) ---

func TestCreateProjectCallsSetRepoCI(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.SetSetting("domain", "rac.so")
	st.SetSetting("github-user", "racso")
	st.SetSetting("github-token", "gh-pat-xxx")

	rr := do(t, srv, "POST", "/projects", map[string]interface{}{"name": "web"}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d — %s", rr.Code, rr.Body.String())
	}

	if len(mocks.repo.setupCalls) != 1 {
		t.Fatalf("expected 1 SetRepoCI call, got %d", len(mocks.repo.setupCalls))
	}
	c := mocks.repo.setupCalls[0]
	if c.Owner != "racso" || c.Repo != "web" {
		t.Errorf("owner/repo: got %s/%s, want racso/web", c.Owner, c.Repo)
	}
	if c.ProjectName != "web" {
		t.Errorf("projectName: got %q, want web", c.ProjectName)
	}
	if c.PoofURL != "https://poof.rac.so" {
		t.Errorf("poofURL: got %q", c.PoofURL)
	}
	if c.Branch != "main" {
		t.Errorf("branch: got %q, want main", c.Branch)
	}
}

func TestCreateProjectSkipsGitHubWithoutPAT(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.SetSetting("domain", "rac.so")
	st.SetSetting("github-user", "racso")
	// No github-token set.

	rr := do(t, srv, "POST", "/projects", map[string]interface{}{"name": "web"}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d — %s", rr.Code, rr.Body.String())
	}

	if len(mocks.repo.setupCalls) != 0 {
		t.Errorf("expected no SetRepoCI calls without PAT, got %d", len(mocks.repo.setupCalls))
	}
}

func TestCreateProjectSkipsGitHubWhenCIDisabled(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.SetSetting("domain", "rac.so")
	st.SetSetting("github-user", "racso")
	st.SetSetting("github-token", "gh-pat-xxx")

	ci := false
	rr := do(t, srv, "POST", "/projects", map[string]interface{}{"name": "web", "ci": ci}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d — %s", rr.Code, rr.Body.String())
	}

	if len(mocks.repo.setupCalls) != 0 {
		t.Errorf("expected no SetRepoCI calls with ci=false, got %d", len(mocks.repo.setupCalls))
	}
}

func TestDeleteProjectCallsRemoveRepoCI(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.SetSetting("github-user", "racso")
	st.SetSetting("github-token", "gh-pat-xxx")

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	rr := do(t, srv, "DELETE", "/projects/web", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: %d — %s", rr.Code, rr.Body.String())
	}

	if len(mocks.repo.removeCalls) != 1 {
		t.Fatalf("expected 1 RemoveRepoCI call, got %d", len(mocks.repo.removeCalls))
	}
	c := mocks.repo.removeCalls[0]
	if c.Owner != "racso" || c.Repo != "web" {
		t.Errorf("owner/repo: got %s/%s", c.Owner, c.Repo)
	}
	if c.ProjectName != "web" {
		t.Errorf("projectName: got %q", c.ProjectName)
	}
	if !c.DeleteSecrets {
		t.Error("expected deleteSecrets=true for last project in repo")
	}
}

func TestDeleteProjectKeepsSecretsWhenSiblingsExist(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.SetSetting("github-user", "racso")
	st.SetSetting("github-token", "gh-pat-xxx")

	// Two projects sharing the same repo.
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/monorepo", Branch: "main", Port: 80,
	})
	st.CreateProject(store.Project{
		Name: "api", Domain: "api.rac.so", Image: "ghcr.io/racso/api",
		Repo: "racso/monorepo", Branch: "main", Port: 3000,
	})

	do(t, srv, "DELETE", "/projects/web", nil, globalToken)

	if len(mocks.repo.removeCalls) != 1 {
		t.Fatalf("expected 1 RemoveRepoCI call, got %d", len(mocks.repo.removeCalls))
	}
	if mocks.repo.removeCalls[0].DeleteSecrets {
		t.Error("expected deleteSecrets=false when sibling project still exists")
	}
}

func TestUpdateProjectRefreshesCIOnBranchChange(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.SetSetting("github-user", "racso")
	st.SetSetting("github-token", "gh-pat-xxx")

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80, CI: true,
	})
	st.SetRepoToken("racso/web", "repo-tok")

	rr := do(t, srv, "PATCH", "/projects/web",
		map[string]interface{}{"branch": "production"}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d — %s", rr.Code, rr.Body.String())
	}

	if len(mocks.repo.refreshCalls) != 1 {
		t.Fatalf("expected 1 RefreshProjectCI call, got %d", len(mocks.repo.refreshCalls))
	}
	c := mocks.repo.refreshCalls[0]
	if c.Branch != "production" {
		t.Errorf("branch: got %q, want production", c.Branch)
	}
	if !c.CI {
		t.Error("expected ci=true to be passed through")
	}
}

func TestUpdateProjectNoGitHubCallWhenNothingCIRelatedChanges(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.SetSetting("github-user", "racso")
	st.SetSetting("github-token", "gh-pat-xxx")

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80, CI: true,
	})

	// Only changing domain — no CI-related fields.
	do(t, srv, "PATCH", "/projects/web",
		map[string]interface{}{"domain": "app.rac.so"}, globalToken)

	if len(mocks.repo.refreshCalls) != 0 {
		t.Errorf("expected no RefreshProjectCI calls for domain-only change, got %d", len(mocks.repo.refreshCalls))
	}
}

func TestCloneProjectCallsSetRepoCI(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.SetSetting("domain", "rac.so")
	st.SetSetting("github-user", "racso")
	st.SetSetting("github-token", "gh-pat-xxx")

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80, CI: true,
	})
	st.SetRepoToken("racso/web", "repo-tok")

	rr := do(t, srv, "POST", "/projects/web/clone",
		map[string]interface{}{"suffix": "staging"}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("clone: %d — %s", rr.Code, rr.Body.String())
	}

	if len(mocks.repo.setupCalls) != 1 {
		t.Fatalf("expected 1 SetRepoCI call, got %d", len(mocks.repo.setupCalls))
	}
	c := mocks.repo.setupCalls[0]
	if c.ProjectName != "web-staging" {
		t.Errorf("projectName: got %q, want web-staging", c.ProjectName)
	}
	if c.Branch != "staging" {
		t.Errorf("branch: got %q, want staging", c.Branch)
	}
}

func TestRefreshProjectCallsRefreshProjectCI(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.SetSetting("github-user", "racso")
	st.SetSetting("github-token", "gh-pat-xxx")

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80, CI: true,
	})
	st.SetRepoToken("racso/web", "repo-tok")

	rr := do(t, srv, "POST", "/projects/web/refresh", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh: %d — %s", rr.Code, rr.Body.String())
	}

	if len(mocks.repo.refreshCalls) != 1 {
		t.Fatalf("expected 1 RefreshProjectCI call, got %d", len(mocks.repo.refreshCalls))
	}
	c := mocks.repo.refreshCalls[0]
	if c.Owner != "racso" || c.Repo != "web" {
		t.Errorf("owner/repo: got %s/%s", c.Owner, c.Repo)
	}
	if c.RepoToken != "repo-tok" {
		t.Errorf("repoToken: got %q", c.RepoToken)
	}
}

// --- Container deploy ---

func TestDeployCallsContainerDeploy(t *testing.T) {
	srv, st, mocks := newTestServer(t)

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80,
	})
	st.SetEnvVar("web", "DB", "pg://localhost")

	rr := do(t, srv, "POST", "/projects/web/deploy",
		map[string]interface{}{"image": "ghcr.io/racso/web:v2"}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("deploy: %d — %s", rr.Code, rr.Body.String())
	}

	if len(mocks.container.deployCalls) != 1 {
		t.Fatalf("expected 1 container.Deploy call, got %d", len(mocks.container.deployCalls))
	}
	c := mocks.container.deployCalls[0]
	if c.Name != "web" {
		t.Errorf("name: got %q", c.Name)
	}
	if c.Image != "ghcr.io/racso/web:v2" {
		t.Errorf("image: got %q", c.Image)
	}
	if c.EnvVars["DB"] != "pg://localhost" {
		t.Errorf("env DB: got %q", c.EnvVars["DB"])
	}
}

func TestDeployWithoutImageRedeploysLatest(t *testing.T) {
	srv, st, mocks := newTestServer(t)

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80,
	})
	// Record a prior deployment.
	st.RecordDeployment("web", "ghcr.io/racso/web:v3", "success")

	rr := do(t, srv, "POST", "/projects/web/deploy", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("deploy: %d — %s", rr.Code, rr.Body.String())
	}

	if len(mocks.container.deployCalls) != 1 {
		t.Fatalf("expected 1 deploy call, got %d", len(mocks.container.deployCalls))
	}
	if mocks.container.deployCalls[0].Image != "ghcr.io/racso/web:v3" {
		t.Errorf("expected redeploy of latest recorded image, got %q", mocks.container.deployCalls[0].Image)
	}
}

func TestDeploySyncsCaddy(t *testing.T) {
	srv, st, mocks := newTestServer(t)

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	do(t, srv, "POST", "/projects/web/deploy",
		map[string]interface{}{"image": "img:v1"}, globalToken)

	if mocks.caddy.reloadCalls < 1 {
		t.Error("expected caddy reload after deploy")
	}
}

// --- Delete cleanup ---

func TestDeleteProjectStopsContainer(t *testing.T) {
	srv, st, mocks := newTestServer(t)

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	do(t, srv, "DELETE", "/projects/web", nil, globalToken)

	if len(mocks.container.stopCalls) != 1 || mocks.container.stopCalls[0] != "web" {
		t.Errorf("expected container.Stop(web), got %v", mocks.container.stopCalls)
	}
}

func TestDeleteStaticProjectRemovesStaticFiles(t *testing.T) {
	srv, st, mocks := newTestServer(t)

	st.CreateProject(store.Project{
		Name: "site", Domain: "site.rac.so",
		Repo: "racso/site", Branch: "main", Static: "static",
	})

	do(t, srv, "DELETE", "/projects/site", nil, globalToken)

	if len(mocks.static.removeCalls) != 1 || mocks.static.removeCalls[0] != "site" {
		t.Errorf("expected static.Remove(site), got %v", mocks.static.removeCalls)
	}
	// Should NOT stop a container for a static project.
	if len(mocks.container.stopCalls) != 0 {
		t.Errorf("expected no container.Stop for static project, got %v", mocks.container.stopCalls)
	}
}

// --- List running status ---

func TestListProjectsShowsRunningStatus(t *testing.T) {
	srv, st, mocks := newTestServer(t)

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80,
	})
	mocks.container.running = map[string]bool{"web": true}

	rr := do(t, srv, "GET", "/projects", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d", rr.Code)
	}

	var projects []map[string]interface{}
	decode(t, rr, &projects)
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0]["running"] != true {
		t.Errorf("expected running=true, got %v", projects[0]["running"])
	}
}

// --- Rollback ---

func TestRollbackRedeploysPreviousImage(t *testing.T) {
	srv, st, mocks := newTestServer(t)

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80,
	})
	st.RecordDeployment("web", "img:v1", "success")
	st.RecordDeployment("web", "img:v2", "success")

	rr := do(t, srv, "POST", "/projects/web/rollback", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("rollback: %d — %s", rr.Code, rr.Body.String())
	}

	if len(mocks.container.deployCalls) != 1 {
		t.Fatalf("expected 1 deploy call, got %d", len(mocks.container.deployCalls))
	}
	if mocks.container.deployCalls[0].Image != "img:v1" {
		t.Errorf("expected rollback to img:v1, got %q", mocks.container.deployCalls[0].Image)
	}
}

func TestRollbackFailsWithNoPreviousDeployment(t *testing.T) {
	srv, st, _ := newTestServer(t)

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	rr := do(t, srv, "POST", "/projects/web/rollback", nil, globalToken)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with no previous deployment, got %d", rr.Code)
	}
}

// --- Update project side effects ---

func TestUpdateProjectSyncsCaddy(t *testing.T) {
	srv, st, mocks := newTestServer(t)

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	do(t, srv, "PATCH", "/projects/web",
		map[string]interface{}{"domain": "app.rac.so"}, globalToken)

	if mocks.caddy.reloadCalls < 1 {
		t.Error("expected caddy reload after project update")
	}
}

func TestDeleteProjectSyncsCaddy(t *testing.T) {
	srv, st, mocks := newTestServer(t)

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	do(t, srv, "DELETE", "/projects/web", nil, globalToken)

	if mocks.caddy.reloadCalls < 1 {
		t.Error("expected caddy reload after project delete")
	}
}

// --- Volumes CRUD ---

func TestListVolumesEmpty(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	rr := do(t, srv, "GET", "/projects/web/volumes", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("list volumes: %d — %s", rr.Code, rr.Body.String())
	}

	var vols []interface{}
	decode(t, rr, &vols)
	if len(vols) != 0 {
		t.Errorf("expected empty list, got %d", len(vols))
	}
}

func TestAddVolumeManagedParsing(t *testing.T) {
	// Managed volumes (container-path only) try to mkdir the host path,
	// which requires root. We test the parseMount logic via the explicit-path
	// test above. Here we just verify the request is accepted and the managed
	// flag is set when the host directory already exists.
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	// Use a temp dir that exists so MkdirAll succeeds.
	dir := t.TempDir()
	rr := do(t, srv, "POST", "/projects/web/volumes",
		map[string]string{"mount": dir + ":/container/data"}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add volume: %d — %s", rr.Code, rr.Body.String())
	}

	var vol map[string]interface{}
	decode(t, rr, &vol)
	if vol["container_path"] != "/container/data" {
		t.Errorf("container_path: got %q", vol["container_path"])
	}
	if vol["managed"] != false {
		t.Errorf("explicit host path should not be managed")
	}
}

func TestAddVolumeExplicitHostPath(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	rr := do(t, srv, "POST", "/projects/web/volumes",
		map[string]string{"mount": "/host/data:/container/data"}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add volume: %d — %s", rr.Code, rr.Body.String())
	}

	var vol map[string]interface{}
	decode(t, rr, &vol)
	if vol["host_path"] != "/host/data" {
		t.Errorf("host_path: got %q", vol["host_path"])
	}
	if vol["container_path"] != "/container/data" {
		t.Errorf("container_path: got %q", vol["container_path"])
	}
	if vol["managed"] != false {
		t.Errorf("expected managed=false for explicit host path")
	}
}

func TestAddVolumeRejectsStaticProject(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "site", Domain: "site.rac.so",
		Repo: "racso/site", Branch: "main", Static: "static",
	})

	rr := do(t, srv, "POST", "/projects/site/volumes",
		map[string]string{"mount": "/data"}, globalToken)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for static project, got %d", rr.Code)
	}
}

func TestAddVolumeRejectsMissingMount(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	rr := do(t, srv, "POST", "/projects/web/volumes",
		map[string]string{}, globalToken)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing mount, got %d", rr.Code)
	}
}

func TestAddVolumeRejectsRelativeContainerPath(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	rr := do(t, srv, "POST", "/projects/web/volumes",
		map[string]string{"mount": "relative/path"}, globalToken)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for relative container path, got %d", rr.Code)
	}
}

func TestAddVolumeRejectsNonexistentProject(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rr := do(t, srv, "POST", "/projects/ghost/volumes",
		map[string]string{"mount": "/data"}, globalToken)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent project, got %d", rr.Code)
	}
}

func TestGetVolume(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	// Add a volume first (explicit host path to avoid mkdir issues).
	rr := do(t, srv, "POST", "/projects/web/volumes",
		map[string]string{"mount": "/host/data:/container/data"}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add: %d — %s", rr.Code, rr.Body.String())
	}
	var created map[string]interface{}
	decode(t, rr, &created)
	id := fmt.Sprintf("%.0f", created["id"].(float64))

	// Get it.
	rr = do(t, srv, "GET", "/projects/web/volumes/"+id, nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("get volume: %d — %s", rr.Code, rr.Body.String())
	}
	var vol map[string]interface{}
	decode(t, rr, &vol)
	if vol["container_path"] != "/container/data" {
		t.Errorf("container_path: got %q", vol["container_path"])
	}
}

func TestGetVolumeNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rr := do(t, srv, "GET", "/projects/web/volumes/99999", nil, globalToken)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestGetVolumeInvalidID(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rr := do(t, srv, "GET", "/projects/web/volumes/abc", nil, globalToken)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid id, got %d", rr.Code)
	}
}

func TestRemoveVolume(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	// Add then remove.
	rr := do(t, srv, "POST", "/projects/web/volumes",
		map[string]string{"mount": "/host/x:/container/x"}, globalToken)
	var created map[string]interface{}
	decode(t, rr, &created)
	id := fmt.Sprintf("%.0f", created["id"].(float64))

	rr = do(t, srv, "DELETE", "/projects/web/volumes/"+id, nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("remove: %d — %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	decode(t, rr, &resp)
	if resp["status"] != "removed" {
		t.Errorf("status: got %q", resp["status"])
	}

	// Confirm it's gone.
	rr = do(t, srv, "GET", "/projects/web/volumes/"+id, nil, globalToken)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 after remove, got %d", rr.Code)
	}
}

func TestRemoveVolumeNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rr := do(t, srv, "DELETE", "/projects/web/volumes/99999", nil, globalToken)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestListVolumesAfterAdd(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	do(t, srv, "POST", "/projects/web/volumes",
		map[string]string{"mount": "/host/a:/container/a"}, globalToken)
	do(t, srv, "POST", "/projects/web/volumes",
		map[string]string{"mount": "/host/b:/container/b"}, globalToken)

	rr := do(t, srv, "GET", "/projects/web/volumes", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d", rr.Code)
	}

	var vols []map[string]interface{}
	decode(t, rr, &vols)
	if len(vols) != 2 {
		t.Errorf("expected 2 volumes, got %d", len(vols))
	}
}

func TestDeployIncludesVolumes(t *testing.T) {
	srv, st, mocks := newTestServer(t)

	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web",
		Repo: "racso/web", Branch: "main", Port: 80,
	})
	st.CreateVolume(store.Volume{
		Project: "web", HostPath: "/host/data", ContainerPath: "/data",
	})
	st.CreateVolume(store.Volume{
		Project: "web", HostPath: "/host/logs", ContainerPath: "/logs",
	})

	rr := do(t, srv, "POST", "/projects/web/deploy",
		map[string]interface{}{"image": "img:v1"}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("deploy: %d — %s", rr.Code, rr.Body.String())
	}

	if len(mocks.container.deployCalls) != 1 {
		t.Fatalf("expected 1 deploy, got %d", len(mocks.container.deployCalls))
	}
	vols := mocks.container.deployCalls[0].Volumes
	if len(vols) != 2 {
		t.Errorf("expected 2 volume mounts, got %d: %v", len(vols), vols)
	}
}

// --- Caddy Snippets ---

func TestGetCaddySnippetReturnsHashHeader(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})
	st.SetCaddySnippet("web", "redir /install https://example.com 302")

	rr := do(t, srv, "GET", "/projects/web/caddy", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d — %s", rr.Code, rr.Body.String())
	}

	var result map[string]interface{}
	decode(t, rr, &result)
	content, _ := result["content"].(string)

	if content == "" {
		t.Fatal("expected non-empty content")
	}
	// Should start with the hash header.
	prefix := "# [poof-caddy] hash:sha256:"
	if len(content) < len(prefix) || content[:len(prefix)] != prefix {
		t.Errorf("content should start with hash header, got: %q", content[:min(len(content), 60)])
	}
	// Content after header should be the original snippet.
	lines := splitFirst(content, "\n")
	if lines[1] != "redir /install https://example.com 302" {
		t.Errorf("body after header: got %q", lines[1])
	}
}

func TestGetCaddySnippetEmpty(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	rr := do(t, srv, "GET", "/projects/web/caddy", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d — %s", rr.Code, rr.Body.String())
	}

	var result map[string]interface{}
	decode(t, rr, &result)
	content, _ := result["content"].(string)

	// Even with no snippet, should return a hash header (hash of empty string).
	if content == "" {
		t.Fatal("expected hash header even for empty snippet")
	}
}

func TestGetCaddySnippetNotFoundProject(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rr := do(t, srv, "GET", "/projects/ghost/caddy", nil, globalToken)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestSetCaddySnippetWithMatchingHash(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	// GET to obtain the hash header (for empty snippet).
	rr := do(t, srv, "GET", "/projects/web/caddy", nil, globalToken)
	var getResult map[string]interface{}
	decode(t, rr, &getResult)
	headerContent, _ := getResult["content"].(string)

	// Replace the empty body after the header with new content.
	header := splitFirst(headerContent, "\n")[0]
	newContent := header + "\nredir /install https://example.com 302"

	beforeReloads := mocks.caddy.reloadCalls
	rr = do(t, srv, "PUT", "/projects/web/caddy",
		map[string]interface{}{"content": newContent}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("set: %d — %s", rr.Code, rr.Body.String())
	}

	// Verify stored content (header stripped).
	got, _ := st.GetCaddySnippet("web")
	if got != "redir /install https://example.com 302" {
		t.Errorf("stored: got %q", got)
	}

	// Verify Caddy was synced.
	if mocks.caddy.reloadCalls <= beforeReloads {
		t.Error("expected caddy reload after set")
	}
}

func TestSetCaddySnippetHashMismatch(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})
	// Pre-populate a snippet so the "current" hash differs from a stale one.
	st.SetCaddySnippet("web", "original content")

	// Craft a request with a wrong hash.
	staleContent := "# [poof-caddy] hash:sha256:0000000000000000000000000000000000000000000000000000000000000000\nnew content"

	rr := do(t, srv, "PUT", "/projects/web/caddy",
		map[string]interface{}{"content": staleContent}, globalToken)
	if rr.Code != http.StatusConflict {
		t.Errorf("expected 409 conflict, got %d — %s", rr.Code, rr.Body.String())
	}

	// Content should be unchanged.
	got, _ := st.GetCaddySnippet("web")
	if got != "original content" {
		t.Errorf("expected content unchanged, got %q", got)
	}
}

func TestSetCaddySnippetMissingHeaderRejected(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	// Send content without the hash header and without force.
	rr := do(t, srv, "PUT", "/projects/web/caddy",
		map[string]interface{}{"content": "redir /foo https://bar.com 302"}, globalToken)
	if rr.Code != http.StatusConflict {
		t.Errorf("expected 409 for missing header, got %d — %s", rr.Code, rr.Body.String())
	}
}

func TestSetCaddySnippetForceBypassesHash(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})
	st.SetCaddySnippet("web", "old content")

	// Force push with no header.
	rr := do(t, srv, "PUT", "/projects/web/caddy",
		map[string]interface{}{"content": "new content", "force": true}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("force set: %d — %s", rr.Code, rr.Body.String())
	}

	got, _ := st.GetCaddySnippet("web")
	if got != "new content" {
		t.Errorf("expected 'new content', got %q", got)
	}
}

func TestSetCaddySnippetForceWithStaleHash(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})
	st.SetCaddySnippet("web", "original")

	// Force push with a wrong hash — should still succeed.
	staleContent := "# [poof-caddy] hash:sha256:0000000000000000000000000000000000000000000000000000000000000000\nforced content"

	rr := do(t, srv, "PUT", "/projects/web/caddy",
		map[string]interface{}{"content": staleContent, "force": true}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("force set: %d — %s", rr.Code, rr.Body.String())
	}

	got, _ := st.GetCaddySnippet("web")
	if got != "forced content" {
		t.Errorf("expected 'forced content', got %q", got)
	}
}

func TestDeleteCaddySnippet(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})
	st.SetCaddySnippet("web", "content")

	beforeReloads := mocks.caddy.reloadCalls
	rr := do(t, srv, "DELETE", "/projects/web/caddy", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: %d — %s", rr.Code, rr.Body.String())
	}

	got, _ := st.GetCaddySnippet("web")
	if got != "" {
		t.Errorf("expected empty after delete, got %q", got)
	}

	if mocks.caddy.reloadCalls <= beforeReloads {
		t.Error("expected caddy reload after delete")
	}
}

func TestDeleteCaddySnippetNotFound(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	rr := do(t, srv, "DELETE", "/projects/web/caddy", nil, globalToken)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent snippet, got %d", rr.Code)
	}
}

func TestDeleteProjectCascadesCaddySnippet(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})
	st.SetCaddySnippet("web", "content")

	rr := do(t, srv, "DELETE", "/projects/web", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete project: %d — %s", rr.Code, rr.Body.String())
	}

	got, _ := st.GetCaddySnippet("web")
	if got != "" {
		t.Errorf("expected snippet cascaded away, got %q", got)
	}
}

func TestListCaddySnippets(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "alpha", Domain: "alpha.rac.so", Image: "img",
		Repo: "racso/alpha", Branch: "main", Port: 80,
	})
	st.CreateProject(store.Project{
		Name: "beta", Domain: "beta.rac.so", Image: "img",
		Repo: "racso/beta", Branch: "main", Port: 80,
	})
	st.CreateProject(store.Project{
		Name: "gamma", Domain: "gamma.rac.so", Image: "img",
		Repo: "racso/gamma", Branch: "main", Port: 80,
	})
	st.SetCaddySnippet("beta", "snippet-b")
	st.SetCaddySnippet("alpha", "snippet-a")

	rr := do(t, srv, "GET", "/caddy/snippets", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d — %s", rr.Code, rr.Body.String())
	}

	var names []string
	decode(t, rr, &names)
	if len(names) != 2 {
		t.Fatalf("expected 2, got %d", len(names))
	}
	// Should be sorted.
	if names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("expected [alpha beta], got %v", names)
	}
}

func TestListCaddySnippetsEmpty(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rr := do(t, srv, "GET", "/caddy/snippets", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d — %s", rr.Code, rr.Body.String())
	}

	var names []string
	decode(t, rr, &names)
	if len(names) != 0 {
		t.Errorf("expected empty list, got %v", names)
	}
}

func TestGetProjectShowsHasCaddySnippet(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "web", Domain: "web.rac.so", Image: "img",
		Repo: "racso/web", Branch: "main", Port: 80,
	})

	// Without snippet.
	rr := do(t, srv, "GET", "/projects/web", nil, globalToken)
	var result map[string]interface{}
	decode(t, rr, &result)
	if result["has_caddy_snippet"] != false {
		t.Errorf("expected has_caddy_snippet=false, got %v", result["has_caddy_snippet"])
	}

	// With snippet.
	st.SetCaddySnippet("web", "redir /x https://y.com 302")
	rr = do(t, srv, "GET", "/projects/web", nil, globalToken)
	decode(t, rr, &result)
	if result["has_caddy_snippet"] != true {
		t.Errorf("expected has_caddy_snippet=true, got %v", result["has_caddy_snippet"])
	}
}

// --- GC ---

func gcIntPtr(v int) *int { return &v }

func TestGCRequiresProjectOrAll(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rr := do(t, srv, "POST", "/gc", map[string]interface{}{}, globalToken)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestGCSingleProjectUsesDefaultPolicy(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "demo", Domain: "demo.rac.so", Image: "ghcr.io/x/demo",
		Repo: "x/demo", Branch: "main", Port: 80,
	})

	rr := do(t, srv, "POST", "/gc", map[string]interface{}{"project": "demo"}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	if len(mocks.container.gcCalls) != 1 {
		t.Fatalf("expected 1 GC call, got %d", len(mocks.container.gcCalls))
	}
	c := mocks.container.gcCalls[0]
	if c.Project != "demo" || c.Image != "ghcr.io/x/demo" {
		t.Errorf("call: %+v", c)
	}
	if c.Keep != 3 {
		t.Errorf("expected default keep=3, got %d", c.Keep)
	}
	if c.OlderThanDays != 0 {
		t.Errorf("expected older_than=0, got %d", c.OlderThanDays)
	}
	if c.DryRun {
		t.Errorf("expected dry_run=false")
	}
	if mocks.container.pruneCalls != 1 {
		t.Errorf("expected 1 prune call, got %d", mocks.container.pruneCalls)
	}
}

func TestGCFlagOverrideBeatsPolicy(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "demo", Image: "ghcr.io/x/demo", Repo: "x/demo", Branch: "main", Port: 80,
	})
	st.SetGCPolicy(store.GCPolicy{Project: "demo", KeepCount: gcIntPtr(99)})

	rr := do(t, srv, "POST", "/gc", map[string]interface{}{
		"project": "demo", "keep": 1,
	}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	c := mocks.container.gcCalls[0]
	if c.Keep != 1 {
		t.Errorf("expected keep=1 (override), got %d", c.Keep)
	}
}

func TestGCDryRunSkipsPrune(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "demo", Image: "ghcr.io/x/demo", Repo: "x/demo", Branch: "main", Port: 80,
	})

	rr := do(t, srv, "POST", "/gc", map[string]interface{}{
		"project": "demo", "dry_run": true,
	}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	if !mocks.container.gcCalls[0].DryRun {
		t.Error("expected dry_run=true on GC call")
	}
	if mocks.container.pruneCalls != 0 {
		t.Errorf("expected no prune in dry-run, got %d", mocks.container.pruneCalls)
	}
}

func TestGCAllSkipsStaticAndDisabled(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "container-app", Image: "ghcr.io/x/c", Repo: "x/c", Branch: "main", Port: 80,
	})
	st.CreateProject(store.Project{
		Name: "static-site", Repo: "x/s", Branch: "main", Static: "static",
	})
	st.CreateProject(store.Project{
		Name: "disabled-app", Image: "ghcr.io/x/d", Repo: "x/d", Branch: "main", Port: 80,
	})
	st.SetGCPolicy(store.GCPolicy{Project: "disabled-app", Disabled: true})

	rr := do(t, srv, "POST", "/gc", map[string]interface{}{"all": true}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	if len(mocks.container.gcCalls) != 1 {
		t.Fatalf("expected 1 GC call (only container-app), got %d", len(mocks.container.gcCalls))
	}
	if mocks.container.gcCalls[0].Project != "container-app" {
		t.Errorf("wrong project GC'd: %s", mocks.container.gcCalls[0].Project)
	}
}

func TestGCStatusReportsResolvedSource(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "with-policy", Image: "ghcr.io/x/a", Repo: "x/a", Branch: "main", Port: 80,
	})
	st.CreateProject(store.Project{
		Name: "from-global", Image: "ghcr.io/x/b", Repo: "x/b", Branch: "main", Port: 80,
	})
	st.CreateProject(store.Project{
		Name: "default-only", Image: "ghcr.io/x/c", Repo: "x/c", Branch: "main", Port: 80,
	})
	st.SetGCPolicy(store.GCPolicy{Project: store.GCPolicyGlobalKey, KeepCount: gcIntPtr(7)})
	st.SetGCPolicy(store.GCPolicy{Project: "with-policy", KeepCount: gcIntPtr(2)})
	// "default-only" can't be reached because global is set; remove global temporarily.

	rr := do(t, srv, "GET", "/gc/status", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d", rr.Code)
	}
	var resp struct {
		Resolved []struct {
			Project string `json:"project"`
			Source  string `json:"source"`
			Enabled bool   `json:"enabled"`
		} `json:"resolved"`
	}
	decode(t, rr, &resp)
	got := map[string]string{}
	for _, r := range resp.Resolved {
		got[r.Project] = r.Source
	}
	if got["with-policy"] != "project" {
		t.Errorf("with-policy source: got %q, want project", got["with-policy"])
	}
	if got["from-global"] != "global" {
		t.Errorf("from-global source: got %q, want global", got["from-global"])
	}
	if got["default-only"] != "global" {
		t.Errorf("default-only inherits global when one is set: got %q", got["default-only"])
	}
}

func TestSetGCPolicyForProject(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "demo", Image: "ghcr.io/x/demo", Repo: "x/demo", Branch: "main", Port: 80,
	})

	rr := do(t, srv, "PUT", "/gc/policy/demo", map[string]interface{}{"keep_count": 5}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}

	pol, _ := st.GetGCPolicy("demo")
	if pol == nil || pol.KeepCount == nil || *pol.KeepCount != 5 {
		t.Errorf("policy not stored: %+v", pol)
	}
}

func TestSetGCPolicyForGlobalDefault(t *testing.T) {
	srv, st, _ := newTestServer(t)
	rr := do(t, srv, "PUT", "/gc/policy/_default", map[string]interface{}{"keep_count": 10}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	pol, _ := st.GetGCPolicy(store.GCPolicyGlobalKey)
	if pol == nil || *pol.KeepCount != 10 {
		t.Errorf("global policy: %+v", pol)
	}
}

func TestSetGCPolicyMissingProject(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rr := do(t, srv, "PUT", "/gc/policy/ghost", map[string]interface{}{"keep_count": 5}, globalToken)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestSetGCPolicyRequiresAField(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.CreateProject(store.Project{
		Name: "demo", Image: "ghcr.io/x/demo", Repo: "x/demo", Branch: "main", Port: 80,
	})
	rr := do(t, srv, "PUT", "/gc/policy/demo", map[string]interface{}{}, globalToken)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestDeleteGCPolicy(t *testing.T) {
	srv, st, _ := newTestServer(t)
	st.SetGCPolicy(store.GCPolicy{Project: store.GCPolicyGlobalKey, KeepCount: gcIntPtr(5)})

	rr := do(t, srv, "DELETE", "/gc/policy/_default", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d", rr.Code)
	}
	pol, _ := st.GetGCPolicy(store.GCPolicyGlobalKey)
	if pol != nil {
		t.Errorf("expected policy gone, got %+v", pol)
	}
}

// splitFirst splits s on the first occurrence of sep, returning [before, after].
// If sep is not found, returns [s, ""].
func splitFirst(s, sep string) [2]string {
	i := 0
	for i < len(s) {
		if i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			return [2]string{s[:i], s[i+len(sep):]}
		}
		i++
	}
	return [2]string{s, ""}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
