package server_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/racso/poof/server"
	"github.com/racso/poof/store"
)

// completeLogin runs the device flow against the mock until the server has a
// stored login, and returns the final GET /auth/github body.
func completeLogin(t *testing.T, srv *server.Server, mocks *testMocks, tok server.UserToken, login string) map[string]interface{} {
	t.Helper()
	mocks.github.pollResults = []mockPoll{{tok: tok}}
	mocks.github.logins = map[string]string{tok.AccessToken: login}
	rr := do(t, srv, "POST", "/auth/github/device", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("start device flow: %d %s", rr.Code, rr.Body.String())
	}
	var started map[string]interface{}
	decode(t, rr, &started)
	if started["user_code"] != "ABCD-1234" || started["verification_uri"] == "" {
		t.Fatalf("unexpected start response: %v", started)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rr = do(t, srv, "GET", "/auth/github", nil, globalToken)
		var st map[string]interface{}
		decode(t, rr, &st)
		if st["pending"] != true {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("login never completed: %v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func freshToken(access string) server.UserToken {
	return server.UserToken{
		AccessToken: access, ExpiresAt: time.Now().Add(8 * time.Hour),
		RefreshToken: "ghr_" + access, RefreshExpiresAt: time.Now().Add(180 * 24 * time.Hour),
	}
}

func TestDeviceLoginStoresCredentialAndUsesItForCI(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.SetSetting("domain", "rac.so")

	status := completeLogin(t, srv, mocks, freshToken("ghu_one"), "Racso")
	if status["source"] != "login" || status["login"] != "Racso" {
		t.Fatalf("status after login: %v", status)
	}
	if u, _ := st.GetSetting("github-user"); u != "Racso" {
		t.Errorf("login should become the default github-user, got %q", u)
	}
	// The blob is sealed: the access token must not sit in the database.
	if raw, _ := st.GetSetting("github-login"); raw == "" || strings.Contains(raw, "ghu_one") {
		t.Errorf("login blob missing or stored in clear: %q", raw)
	}
	// Nor appear in GET /config.
	rr := do(t, srv, "GET", "/config", nil, globalToken)
	if strings.Contains(rr.Body.String(), "github-login") || strings.Contains(rr.Body.String(), "ghu_one") {
		t.Errorf("GET /config exposes the login: %s", rr.Body.String())
	}

	rr = do(t, srv, "POST", "/projects", map[string]interface{}{"name": "web"}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if len(mocks.github.auths) != 1 || mocks.github.auths[0].Token != "ghu_one" || mocks.github.auths[0].Source != "login" {
		t.Errorf("CI setup should use the login token: %+v", mocks.github.auths)
	}
}

func TestLoginTokenIsRefreshedNearExpiry(t *testing.T) {
	srv, _, mocks := newTestServer(t)
	expiring := freshToken("ghu_old")
	expiring.ExpiresAt = time.Now().Add(2 * time.Minute)
	completeLogin(t, srv, mocks, expiring, "Racso")

	mocks.github.refreshTok = freshToken("ghu_new")
	rr := do(t, srv, "POST", "/projects", map[string]interface{}{"name": "web"}, globalToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if len(mocks.github.refreshed) != 1 || mocks.github.refreshed[0] != "ghr_ghu_old" {
		t.Fatalf("expected one refresh with the stored refresh token, got %v", mocks.github.refreshed)
	}
	if got := mocks.github.auths[0].Token; got != "ghu_new" {
		t.Errorf("CI setup should use the refreshed token, got %q", got)
	}
	// The new pair is persisted: a second write refreshes nothing.
	do(t, srv, "POST", "/projects", map[string]interface{}{"name": "api"}, globalToken)
	if len(mocks.github.refreshed) != 1 {
		t.Errorf("refreshed again after persisting the new pair: %v", mocks.github.refreshed)
	}
}

func TestExpiredLoginFallsBackToPATThenToNothing(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	dead := freshToken("ghu_dead")
	dead.ExpiresAt = time.Now().Add(-time.Hour)
	dead.RefreshExpiresAt = time.Now().Add(-time.Minute)
	completeLogin(t, srv, mocks, dead, "Racso")
	st.CreateProject(store.Project{Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web", Repo: "racso/web", Branch: "main", Port: 80, CI: true})

	rr := do(t, srv, "POST", "/projects/web/refresh", nil, globalToken)
	if rr.Code != http.StatusPreconditionFailed || !strings.Contains(rr.Body.String(), "poof login") {
		t.Fatalf("expired login and no PAT should ask for 'poof login': %d %s", rr.Code, rr.Body.String())
	}
	if len(mocks.github.refreshed) != 0 {
		t.Errorf("an expired refresh token must not be exchanged: %v", mocks.github.refreshed)
	}

	st.SetSetting("github-token", "github_pat_fine")
	rr = do(t, srv, "POST", "/projects/web/refresh", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh with PAT: %d %s", rr.Code, rr.Body.String())
	}
	if a := mocks.github.auths[len(mocks.github.auths)-1]; a.Token != "github_pat_fine" || a.Source != "token" {
		t.Errorf("expected the PAT to be used, got %+v", a)
	}
}

func TestLogoutClearsLogin(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	completeLogin(t, srv, mocks, freshToken("ghu_one"), "Racso")

	rr := do(t, srv, "DELETE", "/auth/github", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("logout: %d", rr.Code)
	}
	if raw, _ := st.GetSetting("github-login"); raw != "" {
		t.Error("login blob should be gone")
	}
	rr = do(t, srv, "GET", "/auth/github", nil, globalToken)
	if !strings.Contains(rr.Body.String(), `"source":"none"`) {
		t.Errorf("status after logout: %s", rr.Body.String())
	}
}

func TestDeviceLoginReportsRefusal(t *testing.T) {
	srv, _, mocks := newTestServer(t)
	mocks.github.pollResults = []mockPoll{{err: errors.New("access denied")}}
	rr := do(t, srv, "POST", "/auth/github/device", nil, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("start: %d", rr.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rr = do(t, srv, "GET", "/auth/github", nil, globalToken)
		var st map[string]interface{}
		decode(t, rr, &st)
		if st["pending"] != true {
			if st["pending_error"] != "access denied" || st["source"] != "none" {
				t.Fatalf("status: %v", st)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("flow never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDeployWithoutJobTokenUsesPATOrNothing(t *testing.T) {
	srv, st, mocks := newTestServer(t)
	st.SetSetting("github-user", "racso")
	st.CreateProject(store.Project{Name: "web", Domain: "web.rac.so", Image: "ghcr.io/racso/web", Repo: "racso/web", Branch: "main", Port: 80})

	rr := do(t, srv, "POST", "/projects/web/deploy", map[string]interface{}{"image": "ghcr.io/racso/web:v2"}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rr.Code, rr.Body.String())
	}
	if c := mocks.container.deployCalls[0]; c.RegistryUser != "" || c.RegistryToken != "" {
		t.Errorf("no credential configured: pull must be anonymous, got %s / %s", c.RegistryUser, c.RegistryToken)
	}

	st.SetSetting("github-token", "github_pat_x")
	rr = do(t, srv, "POST", "/projects/web/deploy", map[string]interface{}{"image": "ghcr.io/racso/web:v3"}, globalToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rr.Code, rr.Body.String())
	}
	if c := mocks.container.deployCalls[1]; c.RegistryUser != "racso" || c.RegistryToken != "github_pat_x" {
		t.Errorf("PAT should be used for a manual pull, got %s / %s", c.RegistryUser, c.RegistryToken)
	}
}
