package server_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/racso/poof/server"
	"github.com/racso/poof/store"
)

// fakeGitHub signs Actions-style OIDC tokens and serves the matching JWKS.
type fakeGitHub struct {
	key    *rsa.PrivateKey
	issuer string
	jwks   *httptest.Server
}

func newFakeGitHub(t *testing.T, srv *server.Server) *fakeGitHub {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGitHub{key: key, issuer: "https://token.actions.test"}
	g.jwks = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{{
				"kty": "RSA", "kid": "gh",
				"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}},
		})
	}))
	t.Cleanup(g.jwks.Close)
	srv.SetOIDCEndpoints(g.issuer, g.jwks.URL)
	return g
}

// token returns a valid token for repo, with repository_id repoID.
func (g *fakeGitHub) token(t *testing.T, repo, repoID string) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "gh"})
	claims, _ := json.Marshal(map[string]interface{}{
		"iss":           g.issuer,
		"aud":           "https://poof.rac.so",
		"exp":           time.Now().Add(5 * time.Minute).Unix(),
		"repository":    repo,
		"repository_id": repoID,
		"run_id":        "1",
		"actor":         "ci",
	})
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newOIDCProject(t *testing.T) (*server.Server, *fakeGitHub) {
	t.Helper()
	srv, st, _ := newTestServer(t)
	p := store.Project{
		Name: "demo", Domain: "demo.rac.so", Image: "img:v1",
		Repo: "racso/demo", Branch: "main", Port: 8080,
	}
	if err := st.CreateProject(p); err != nil {
		t.Fatalf("create: %v", err)
	}
	return srv, newFakeGitHub(t, srv)
}

func TestDeployAcceptsOIDCTokenForProjectRepo(t *testing.T) {
	srv, gh := newOIDCProject(t)
	// Repository claim matches case-insensitively (GitHub preserves the
	// owner's casing; projects are registered however the user typed them).
	rr := do(t, srv, "POST", "/projects/demo/deploy",
		map[string]interface{}{"image": "img:v2"}, gh.token(t, "Racso/demo", "42"))
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("OIDC token for the project's repo should be accepted: %s", rr.Body.String())
	}
}

func TestDeployRejectsOIDCTokenForOtherRepo(t *testing.T) {
	srv, gh := newOIDCProject(t)
	rr := do(t, srv, "POST", "/projects/demo/deploy",
		map[string]interface{}{"image": "img:v2"}, gh.token(t, "racso/other", "43"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "racso/other") {
		t.Errorf("error should name the offending repository: %s", rr.Body.String())
	}
}

func TestDeployPinsRepositoryID(t *testing.T) {
	srv, gh := newOIDCProject(t)
	first := do(t, srv, "POST", "/projects/demo/deploy",
		map[string]interface{}{"image": "img:v2"}, gh.token(t, "racso/demo", "42"))
	if first.Code == http.StatusUnauthorized {
		t.Fatalf("first deploy should pin the id, got 401: %s", first.Body.String())
	}
	// Same name, different repository id: a recreated repo, not ours.
	second := do(t, srv, "POST", "/projects/demo/deploy",
		map[string]interface{}{"image": "img:v3"}, gh.token(t, "racso/demo", "99"))
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a different repository id, got %d", second.Code)
	}
}

func TestDeployRejectsOIDCTokenFromUnknownIssuerKey(t *testing.T) {
	srv, gh := newOIDCProject(t)
	// A second, unrelated signer claiming the same issuer.
	other := &fakeGitHub{issuer: gh.issuer}
	other.key, _ = rsa.GenerateKey(rand.Reader, 2048)
	rr := do(t, srv, "POST", "/projects/demo/deploy",
		map[string]interface{}{"image": "img:v2"}, other.token(t, "racso/demo", "42"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a forged signature, got %d", rr.Code)
	}
}

func TestDeployStaticAcceptsOIDCToken(t *testing.T) {
	srv, st, _ := newTestServer(t)
	p := store.Project{Name: "site", Domain: "site.rac.so", Repo: "racso/site", Branch: "main", Static: "static"}
	if err := st.CreateProject(p); err != nil {
		t.Fatalf("create: %v", err)
	}
	gh := newFakeGitHub(t, srv)
	req := httptest.NewRequest("POST", "/projects/site/deploy/static", strings.NewReader("tarball"))
	req.Header.Set("Authorization", "Bearer "+gh.token(t, "racso/site", "7"))
	req.Header.Set("Content-Type", "application/gzip")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("OIDC token should be accepted on deploy/static: %s", rr.Body.String())
	}
}

func TestDeployPassesRegistryTokenToPull(t *testing.T) {
	srv, st, m := newTestServer(t)
	if err := st.CreateProject(store.Project{Name: "demo", Domain: "demo.rac.so", Image: "img:v1", Repo: "racso/demo", Branch: "main", Port: 8080}); err != nil {
		t.Fatal(err)
	}
	gh := newFakeGitHub(t, srv)
	req := httptest.NewRequest("POST", "/projects/demo/deploy", strings.NewReader(`{"image":"ghcr.io/racso/demo:abc"}`))
	req.Header.Set("Authorization", "Bearer "+gh.token(t, "racso/demo", "42"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(server.RegistryTokenHeader, "ghs_jobtoken")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rr.Code, rr.Body.String())
	}
	if len(m.container.deployCalls) != 1 {
		t.Fatalf("expected one deploy, got %d", len(m.container.deployCalls))
	}
	d := m.container.deployCalls[0]
	if d.RegistryToken != "ghs_jobtoken" || d.RegistryUser != "x-access-token" {
		t.Errorf("pull should use the job token as x-access-token, got %q / %q", d.RegistryUser, d.RegistryToken)
	}
}
