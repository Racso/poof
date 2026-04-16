package server_test

import (
	"bytes"
	"encoding/json"
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
}

type mockRemoveCall struct {
	Owner, Repo, ProjectName string
	DeleteSecrets            bool
}

type mockRefreshCall struct {
	Owner, Repo, ProjectName string
	CI                       bool
	PoofURL, RepoToken, Branch, Image, Folder, Static string
	DeleteSecrets                                      bool
}

func (m *mockRepoManager) SetRepoCI(owner, repo, projectName, poofURL, poofToken, branch, image, folder, static string) error {
	m.setupCalls = append(m.setupCalls, mockSetupCall{owner, repo, projectName, poofURL, poofToken, branch, image, folder, static})
	return nil
}

func (m *mockRepoManager) RemoveRepoCI(owner, repo, projectName string, deleteSecrets bool) error {
	m.removeCalls = append(m.removeCalls, mockRemoveCall{owner, repo, projectName, deleteSecrets})
	return nil
}

func (m *mockRepoManager) RefreshProjectCI(owner, repo, projectName string, ci bool, poofURL, repoToken, branch, image, folder, static string, deleteSecrets bool) error {
	m.refreshCalls = append(m.refreshCalls, mockRefreshCall{owner, repo, projectName, ci, poofURL, repoToken, branch, image, folder, static, deleteSecrets})
	return nil
}

// --- Mock ContainerManager ---

type mockContainerManager struct {
	deployCalls []server.ContainerDeployConfig
	stopCalls   []string
	running     map[string]bool
	logs        map[string]string
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
