package server

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
)

// testIssuer signs tokens and serves their JWKS, standing in for GitHub.
type testIssuer struct {
	key    *rsa.PrivateKey
	kid    string
	issuer string
	jwks   *httptest.Server
	hits   int
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ti := &testIssuer{key: key, kid: "k1", issuer: "https://issuer.test"}
	ti.jwks = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ti.hits++
		pub := ti.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{{
				"kty": "RSA",
				"kid": ti.kid,
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	}))
	t.Cleanup(ti.jwks.Close)
	return ti
}

// token signs claims; a nil override keeps a valid default set.
func (ti *testIssuer) token(t *testing.T, override map[string]interface{}) string {
	t.Helper()
	claims := map[string]interface{}{
		"iss":              ti.issuer,
		"aud":              "https://poof.rac.so",
		"exp":              time.Now().Add(5 * time.Minute).Unix(),
		"nbf":              time.Now().Add(-time.Minute).Unix(),
		"repository":       "Racso/demo",
		"repository_owner": "Racso",
		"repository_id":    "42",
		"ref":              "refs/heads/main",
		"run_id":           "7",
		"actor":            "racso",
	}
	for k, v := range override {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	return ti.sign(t, map[string]string{"alg": "RS256", "typ": "JWT", "kid": ti.kid}, claims)
}

func (ti *testIssuer) sign(t *testing.T, header map[string]string, claims map[string]interface{}) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, ti.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (ti *testIssuer) verifier() *oidcVerifier {
	return newOIDCVerifier(ti.issuer, ti.jwks.URL)
}

func TestOIDCVerifyAcceptsValidToken(t *testing.T) {
	ti := newTestIssuer(t)
	claims, err := ti.verifier().Verify(ti.token(t, nil), "https://poof.rac.so")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Repository != "Racso/demo" || claims.RepositoryID != "42" || claims.RunID != "7" {
		t.Errorf("unexpected claims: %+v", claims)
	}
}

func TestOIDCVerifyAcceptsAudienceArray(t *testing.T) {
	ti := newTestIssuer(t)
	tok := ti.token(t, map[string]interface{}{"aud": []string{"other", "https://poof.rac.so"}})
	if _, err := ti.verifier().Verify(tok, "https://poof.rac.so"); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestOIDCVerifyRejects(t *testing.T) {
	ti := newTestIssuer(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	forged := &testIssuer{key: other, kid: ti.kid, issuer: ti.issuer}

	cases := map[string]struct {
		token string
		want  string
	}{
		"wrong audience":  {ti.token(t, map[string]interface{}{"aud": "https://elsewhere"}), "audience"},
		"wrong issuer":    {ti.token(t, map[string]interface{}{"iss": "https://evil.test"}), "issuer"},
		"expired":         {ti.token(t, map[string]interface{}{"exp": time.Now().Add(-5 * time.Minute).Unix()}), "expired"},
		"not yet valid":   {ti.token(t, map[string]interface{}{"nbf": time.Now().Add(10 * time.Minute).Unix()}), "not yet valid"},
		"no repository":   {ti.token(t, map[string]interface{}{"repository": nil}), "repository"},
		"forged key":      {forged.token(t, nil), "signature"},
		"unknown kid":     {ti.sign(t, map[string]string{"alg": "RS256", "kid": "nope"}, map[string]interface{}{"iss": ti.issuer}), "unknown signing key"},
		"wrong algorithm": {ti.sign(t, map[string]string{"alg": "HS256", "kid": ti.kid}, map[string]interface{}{"iss": ti.issuer}), "algorithm"},
		"garbage":         {"not.a.jwt", "malformed"},
	}
	for name, tc := range cases {
		_, err := ti.verifier().Verify(tc.token, "https://poof.rac.so")
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", name, err, tc.want)
		}
	}
}

func TestOIDCVerifyCachesKeysAndThrottlesRefetch(t *testing.T) {
	ti := newTestIssuer(t)
	v := ti.verifier()
	now := time.Now()
	v.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if _, err := v.Verify(ti.token(t, nil), "https://poof.rac.so"); err != nil {
			t.Fatalf("verify %d: %v", i, err)
		}
	}
	if ti.hits != 1 {
		t.Errorf("expected one JWKS fetch for known key, got %d", ti.hits)
	}

	unknown := ti.sign(t, map[string]string{"alg": "RS256", "kid": "k2"}, map[string]interface{}{"iss": ti.issuer})
	_, _ = v.Verify(unknown, "https://poof.rac.so")
	if ti.hits != 1 {
		t.Errorf("unknown kid within a minute of the last fetch must not refetch, got %d fetches", ti.hits)
	}

	now = now.Add(2 * oidcRefetchInterval)
	_, _ = v.Verify(unknown, "https://poof.rac.so")
	if ti.hits != 2 {
		t.Errorf("unknown kid after the interval should refetch once, got %d fetches", ti.hits)
	}
}

func TestOIDCIssuerOf(t *testing.T) {
	ti := newTestIssuer(t)
	if got := oidcIssuerOf(ti.token(t, nil)); got != ti.issuer {
		t.Errorf("issuer = %q", got)
	}
	for _, bad := range []string{"", "plain-token", "a.b", "a.!!.c"} {
		if got := oidcIssuerOf(bad); got != "" {
			t.Errorf("%q: issuer = %q, want empty", bad, got)
		}
	}
}
