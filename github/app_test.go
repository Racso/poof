package github

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// decodeJWT splits a compact JWT, verifies its RS256 signature against pub,
// and returns the decoded claims.
func decodeJWT(t *testing.T, token string, pub *rsa.PublicKey) map[string]interface{} {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt: want 3 segments, got %d", len(parts))
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("jwt header: %v", err)
	}
	if string(header) != `{"alg":"RS256","typ":"JWT"}` {
		t.Errorf("jwt header: %s", header)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("jwt signature: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("jwt signature does not verify: %v", err)
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("jwt claims: %v", err)
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatalf("jwt claims json: %v", err)
	}
	return claims
}

func TestAppJWT_SignedAndScoped(t *testing.T) {
	key := testKey(t)
	a := newAppAuth("12345", key)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	token, err := a.jwt()
	if err != nil {
		t.Fatal(err)
	}
	claims := decodeJWT(t, token, &key.PublicKey)
	if claims["iss"] != "12345" {
		t.Errorf("iss: got %v", claims["iss"])
	}
	if got := int64(claims["iat"].(float64)); got != now.Add(-time.Minute).Unix() {
		t.Errorf("iat: got %d, want one minute before now", got)
	}
	exp := int64(claims["exp"].(float64))
	if exp-now.Unix() > 600 {
		t.Errorf("exp exceeds GitHub's 10 minute cap: %d seconds", exp-now.Unix())
	}
	if exp <= now.Unix() {
		t.Errorf("exp is not in the future")
	}
}

func TestLoadPrivateKey_PKCS1AndPKCS8(t *testing.T) {
	key := testKey(t)
	dir := t.TempDir()

	pkcs1 := filepath.Join(dir, "pkcs1.pem")
	if err := os.WriteFile(pkcs1, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := filepath.Join(dir, "pkcs8.pem")
	if err := os.WriteFile(pkcs8, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{pkcs1, pkcs8} {
		loaded, err := LoadPrivateKey(path)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		if !loaded.Equal(key) {
			t.Errorf("%s: loaded key differs", filepath.Base(path))
		}
	}

	garbage := filepath.Join(dir, "garbage.pem")
	_ = os.WriteFile(garbage, []byte("not a key"), 0600)
	if _, err := LoadPrivateKey(garbage); err == nil {
		t.Error("garbage file: expected an error")
	}
	if _, err := LoadPrivateKey(filepath.Join(dir, "missing.pem")); err == nil {
		t.Error("missing file: expected an error")
	}
	if _, err := LoadPrivateKey(""); err == nil {
		t.Error("empty path: expected an error")
	}
}

// fakeGitHub is a minimal GitHub App API: installations on two accounts
// (paged, one per page, to exercise Link following) and a token endpoint
// that records what was requested.
type fakeGitHub struct {
	t          *testing.T
	pub        *rsa.PublicKey
	srv        *httptest.Server
	listCalls  atomic.Int32
	mintCalls  atomic.Int32
	lastMint   map[string]interface{}
	lastMintID string
	expiresAt  time.Time
}

func newFakeGitHub(t *testing.T, pub *rsa.PublicKey) *fakeGitHub {
	f := &fakeGitHub{t: t, pub: pub, expiresAt: time.Now().Add(time.Hour)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, r *http.Request) {
		f.listCalls.Add(1)
		f.requireJWT(r)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id": 202, "account": {"login": "silviatorres1"}}]`))
			return
		}
		w.Header().Set("Link", `<`+f.srv.URL+`/app/installations?per_page=100&page=2>; rel="next", <`+f.srv.URL+`/app/installations?per_page=100&page=2>; rel="last"`)
		_, _ = w.Write([]byte(`[{"id": 101, "account": {"login": "Racso"}}]`))
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		f.mintCalls.Add(1)
		f.requireJWT(r)
		f.lastMintID = r.PathValue("id")
		f.lastMint = nil
		if err := json.NewDecoder(r.Body).Decode(&f.lastMint); err != nil {
			t.Errorf("mint body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      "ghs_" + r.PathValue("id"),
			"expires_at": f.expiresAt.UTC().Format(time.RFC3339),
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) requireJWT(r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		f.t.Errorf("%s %s: missing bearer", r.Method, r.URL.Path)
		return
	}
	decodeJWT(f.t, strings.TrimPrefix(auth, "Bearer "), f.pub)
}

func TestAppAuth_RepoTokenDiscoversInstallationAndScopes(t *testing.T) {
	key := testKey(t)
	fake := newFakeGitHub(t, &key.PublicKey)
	a := newAppAuth("12345", key)
	a.apiBase = fake.srv.URL

	tok, err := a.RepoToken("racso", "poof-test")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "ghs_101" {
		t.Errorf("token: got %q, want the Racso installation's", tok)
	}
	if fake.lastMintID != "101" {
		t.Errorf("installation: got %s, want 101 (owner match is case-insensitive)", fake.lastMintID)
	}
	repos, _ := fake.lastMint["repositories"].([]interface{})
	if len(repos) != 1 || repos[0] != "poof-test" {
		t.Errorf("token not scoped to the single repo: %v", fake.lastMint["repositories"])
	}
	perms, _ := fake.lastMint["permissions"].(map[string]interface{})
	want := map[string]string{"contents": "write", "workflows": "write", "secrets": "write", "metadata": "read"}
	if len(perms) != len(want) {
		t.Errorf("permissions: got %v, want %v", perms, want)
	}
	for k, v := range want {
		if perms[k] != v {
			t.Errorf("permission %s: got %v, want %s", k, perms[k], v)
		}
	}

	// Second account, found on the second page.
	tok, err = a.RepoToken("silviatorres1", "galqui")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "ghs_202" {
		t.Errorf("token: got %q, want the silviatorres1 installation's", tok)
	}
	if fake.listCalls.Load() != 2 {
		t.Errorf("installations listed %d times, want 2 (both pages, once)", fake.listCalls.Load())
	}

	if _, err := a.RepoToken("nobody", "x"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("unknown owner: got %v, want 'not installed' error", err)
	}
}

func TestAppAuth_PackagesTokenIsReadOnlyAndUnscoped(t *testing.T) {
	key := testKey(t)
	fake := newFakeGitHub(t, &key.PublicKey)
	a := newAppAuth("12345", key)
	a.apiBase = fake.srv.URL

	if _, err := a.PackagesToken("racso"); err != nil {
		t.Fatal(err)
	}
	if _, has := fake.lastMint["repositories"]; has {
		t.Errorf("packages token must not be repo-scoped: %v", fake.lastMint)
	}
	perms, _ := fake.lastMint["permissions"].(map[string]interface{})
	if len(perms) != 1 || perms["packages"] != "read" {
		t.Errorf("permissions: got %v, want packages:read only", perms)
	}
}

func TestAppAuth_TokensCachedUntilNearExpiry(t *testing.T) {
	key := testKey(t)
	fake := newFakeGitHub(t, &key.PublicKey)
	a := newAppAuth("12345", key)
	a.apiBase = fake.srv.URL
	now := time.Now()
	a.now = func() time.Time { return now }
	fake.expiresAt = now.Add(time.Hour)

	for i := 0; i < 3; i++ {
		if _, err := a.RepoToken("racso", "poof-test"); err != nil {
			t.Fatal(err)
		}
	}
	if fake.mintCalls.Load() != 1 {
		t.Errorf("minted %d times for the same scope, want 1", fake.mintCalls.Load())
	}

	// A different scope is a different token.
	if _, err := a.PackagesToken("racso"); err != nil {
		t.Fatal(err)
	}
	if fake.mintCalls.Load() != 2 {
		t.Errorf("minted %d times, want 2 after a second scope", fake.mintCalls.Load())
	}

	// Within 5 minutes of expiry the cached token is refreshed.
	now = now.Add(56 * time.Minute)
	if _, err := a.RepoToken("racso", "poof-test"); err != nil {
		t.Fatal(err)
	}
	if fake.mintCalls.Load() != 3 {
		t.Errorf("minted %d times, want 3 after the token neared expiry", fake.mintCalls.Load())
	}
}

func TestNextLink(t *testing.T) {
	cases := map[string]string{
		``: "",
		`<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=9>; rel="last"`:  "https://api.github.com/x?page=2",
		`<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=1>; rel="first"`: "",
	}
	for header, want := range cases {
		if got := nextLink(header); got != want {
			t.Errorf("nextLink(%q) = %q, want %q", header, got, want)
		}
	}
}
