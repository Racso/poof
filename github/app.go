package github

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// RegistryUser is the username GHCR expects alongside an installation token.
const RegistryUser = "x-access-token"

// repoPermissions is what `poof add` / `poof refresh` need on one repository:
// commit the workflow file (contents + workflows) and set the Actions secrets.
var repoPermissions = map[string]string{
	"contents":  "write",
	"workflows": "write",
	"secrets":   "write",
	"metadata":  "read",
}

// packagesPermissions is what a registry pull needs, and nothing else.
var packagesPermissions = map[string]string{
	"packages": "read",
}

// jwtLifetime is how long an App JWT is valid. GitHub caps it at 10 minutes;
// staying under leaves room for clock skew in either direction.
const jwtLifetime = 9 * time.Minute

// tokenRefreshMargin is how long before expiry a cached installation token is
// considered stale. Installation tokens live one hour.
const tokenRefreshMargin = 5 * time.Minute

// AppAuth mints GitHub App installation tokens. One App can be installed on
// several accounts; the installation is discovered from the repository owner
// on every first use, so a second account needs no server setting beyond
// installing the App.
//
// Tokens are cached per (owner, repo, permissions) until shortly before
// they expire. Safe for concurrent use.
type AppAuth struct {
	appID   string
	key     *rsa.PrivateKey
	apiBase string
	http    *http.Client
	now     func() time.Time

	mu            sync.Mutex
	installations map[string]int64 // lower-cased owner → installation id
	tokens        map[string]installationToken
}

type installationToken struct {
	token   string
	expires time.Time
}

// NewAppAuth loads the App's private key from keyPath. The key is the PEM
// file GitHub generates for the App (PKCS#1); PKCS#8 is accepted too.
func NewAppAuth(appID, keyPath string) (*AppAuth, error) {
	if appID == "" {
		return nil, fmt.Errorf("github app id is empty")
	}
	key, err := LoadPrivateKey(keyPath)
	if err != nil {
		return nil, err
	}
	return newAppAuth(appID, key), nil
}

func newAppAuth(appID string, key *rsa.PrivateKey) *AppAuth {
	return &AppAuth{
		appID:         appID,
		key:           key,
		apiBase:       apiBase,
		http:          http.DefaultClient,
		now:           time.Now,
		installations: make(map[string]int64),
		tokens:        make(map[string]installationToken),
	}
}

// LoadPrivateKey reads and parses an RSA private key in PEM form.
func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	if path == "" {
		return nil, fmt.Errorf("github app key path is empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read github app key: %w", err)
	}
	return parsePrivateKey(raw)
}

func parsePrivateKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("github app key: no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github app key: not an RSA private key (PKCS#1 or PKCS#8)")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github app key: not an RSA key")
	}
	return key, nil
}

// RepoToken returns an installation token able to manage CI on exactly one
// repository: contents, workflows and secrets write, metadata read.
func (a *AppAuth) RepoToken(owner, repo string) (string, error) {
	return a.token(owner, []string{repo}, repoPermissions)
}

// PackagesToken returns an installation token that can only read the
// owner's packages, for registry pulls.
func (a *AppAuth) PackagesToken(owner string) (string, error) {
	return a.token(owner, nil, packagesPermissions)
}

func (a *AppAuth) token(owner string, repos []string, perms map[string]string) (string, error) {
	cacheKey := tokenCacheKey(owner, repos, perms)

	a.mu.Lock()
	defer a.mu.Unlock()

	if cached, ok := a.tokens[cacheKey]; ok && a.now().Add(tokenRefreshMargin).Before(cached.expires) {
		return cached.token, nil
	}

	installationID, err := a.installationIDLocked(owner)
	if err != nil {
		return "", err
	}
	tok, err := a.mintToken(installationID, repos, perms)
	if err != nil {
		return "", err
	}
	a.tokens[cacheKey] = tok
	return tok.token, nil
}

func tokenCacheKey(owner string, repos []string, perms map[string]string) string {
	keys := make([]string, 0, len(perms))
	for k, v := range perms {
		keys = append(keys, k+"="+v)
	}
	sort.Strings(keys)
	return strings.ToLower(owner) + "\x00" + strings.Join(repos, ",") + "\x00" + strings.Join(keys, ",")
}

// installationIDLocked resolves the App installation that covers owner,
// consulting GET /app/installations on a cache miss. Caller holds a.mu.
func (a *AppAuth) installationIDLocked(owner string) (int64, error) {
	if id, ok := a.installations[strings.ToLower(owner)]; ok {
		return id, nil
	}
	jwt, err := a.jwt()
	if err != nil {
		return 0, err
	}

	type installation struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}

	url := a.apiBase + "/app/installations?per_page=100"
	for url != "" {
		var page []installation
		next, err := a.doJSON("GET", url, jwt, nil, &page)
		if err != nil {
			return 0, fmt.Errorf("list app installations: %w", err)
		}
		for _, inst := range page {
			a.installations[strings.ToLower(inst.Account.Login)] = inst.ID
		}
		url = next
	}

	id, ok := a.installations[strings.ToLower(owner)]
	if !ok {
		return 0, fmt.Errorf("GitHub App is not installed on account %q: install it there and retry", owner)
	}
	return id, nil
}

func (a *AppAuth) mintToken(installationID int64, repos []string, perms map[string]string) (installationToken, error) {
	jwt, err := a.jwt()
	if err != nil {
		return installationToken{}, err
	}
	payload := map[string]interface{}{"permissions": perms}
	if len(repos) > 0 {
		payload["repositories"] = repos
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.apiBase, installationID)
	if _, err := a.doJSON("POST", url, jwt, payload, &out); err != nil {
		return installationToken{}, fmt.Errorf("create installation token: %w", err)
	}
	if out.Token == "" {
		return installationToken{}, fmt.Errorf("create installation token: empty token in response")
	}
	return installationToken{token: out.Token, expires: out.ExpiresAt}, nil
}

// doJSON performs one authenticated request and decodes a JSON response into
// out. It returns the URL of the next page when the response carries a
// `Link: <...>; rel="next"` header.
func (a *AppAuth) doJSON(method, url, bearer string, payload, out interface{}) (next string, err error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GitHub API %s: %s", resp.Status, string(b))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return nextLink(resp.Header.Get("Link")), nil
}

var nextLinkRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func nextLink(header string) string {
	m := nextLinkRe.FindStringSubmatch(header)
	if m == nil {
		return ""
	}
	return m[1]
}

// jwt signs a short-lived RS256 JWT identifying the App, as required by the
// /app/* endpoints. `iat` is backdated a minute to absorb clock drift.
func (a *AppAuth) jwt() (string, error) {
	now := a.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]interface{}{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(jwtLifetime).Unix(),
		"iss": a.appID,
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign app jwt: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
