package server

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GitHub Actions OIDC: every job can ask GitHub for a short-lived RS256 JWT
// that names the repository it runs for. The deploy endpoints accept that
// token in place of a shared secret, so a repo needs nothing stored to prove
// who it is. Verification is signature (against GitHub's published JWKS),
// issuer, audience (this server's public URL) and expiry; the repository
// claim is matched against the project by the caller.
const (
	githubOIDCIssuer  = "https://token.actions.githubusercontent.com"
	githubOIDCJWKSURL = githubOIDCIssuer + "/.well-known/jwks"

	// oidcClockSkew is the tolerance applied to exp and nbf.
	oidcClockSkew = 60 * time.Second
	// oidcRefetchInterval bounds how often an unknown key id triggers a JWKS
	// refresh, so a flood of bad tokens cannot turn into a flood of fetches.
	oidcRefetchInterval = time.Minute
)

// oidcClaims are the GitHub Actions token claims the server reads. Numeric
// ids arrive as strings in GitHub's token.
type oidcClaims struct {
	Issuer          string       `json:"iss"`
	Audience        oidcAudience `json:"aud"`
	Expiry          int64        `json:"exp"`
	NotBefore       int64        `json:"nbf"`
	Repository      string       `json:"repository"`
	RepositoryOwner string       `json:"repository_owner"`
	RepositoryID    string       `json:"repository_id"`
	Ref             string       `json:"ref"`
	SHA             string       `json:"sha"`
	RunID           string       `json:"run_id"`
	Actor           string       `json:"actor"`
	EventName       string       `json:"event_name"`
}

// oidcAudience accepts the `aud` claim as a string or an array of strings,
// both of which the JWT spec allows.
type oidcAudience []string

func (a *oidcAudience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = oidcAudience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("aud: %w", err)
	}
	*a = many
	return nil
}

func (a oidcAudience) contains(want string) bool {
	for _, v := range a {
		if v == want {
			return true
		}
	}
	return false
}

// oidcVerifier verifies GitHub Actions tokens against a cached JWKS.
type oidcVerifier struct {
	issuer  string
	jwksURL string
	client  *http.Client
	now     func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func newOIDCVerifier(issuer, jwksURL string) *oidcVerifier {
	return &oidcVerifier{
		issuer:  issuer,
		jwksURL: jwksURL,
		client:  &http.Client{Timeout: 10 * time.Second},
		now:     time.Now,
	}
}

// oidcIssuerOf returns the unverified `iss` claim of token, or "" when token
// is not shaped like a JWT. It only decides whether a bearer token should be
// treated as OIDC at all; nothing is trusted until Verify runs.
func oidcIssuerOf(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Issuer string `json:"iss"`
	}
	if json.Unmarshal(payload, &c) != nil {
		return ""
	}
	return c.Issuer
}

// Verify checks token's signature, issuer, audience and validity window and
// returns its claims. The error is safe to show to the caller.
func (v *oidcVerifier) Verify(token, audience string) (*oidcClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("malformed token header")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, errors.New("malformed token header")
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported algorithm %q", header.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("malformed token signature")
	}

	key, err := v.key(header.Kid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return nil, errors.New("invalid signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("malformed token payload")
	}
	var claims oidcClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errors.New("malformed token payload")
	}
	if claims.Issuer != v.issuer {
		return nil, fmt.Errorf("unexpected issuer %q", claims.Issuer)
	}
	if !claims.Audience.contains(audience) {
		return nil, fmt.Errorf("audience %v does not include %q", []string(claims.Audience), audience)
	}
	now := v.now()
	if claims.Expiry == 0 || now.After(time.Unix(claims.Expiry, 0).Add(oidcClockSkew)) {
		return nil, errors.New("token expired")
	}
	if claims.NotBefore != 0 && now.Before(time.Unix(claims.NotBefore, 0).Add(-oidcClockSkew)) {
		return nil, errors.New("token not yet valid")
	}
	if claims.Repository == "" {
		return nil, errors.New("token carries no repository claim")
	}
	return &claims, nil
}

// key returns the public key for kid, refreshing the JWKS when the id is
// unknown and the last fetch is old enough.
func (v *oidcVerifier) key(kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	if !v.fetchedAt.IsZero() && v.now().Sub(v.fetchedAt) < oidcRefetchInterval {
		return nil, fmt.Errorf("unknown signing key %q", kid)
	}
	if err := v.fetchLocked(); err != nil {
		return nil, fmt.Errorf("fetch signing keys: %w", err)
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

type jwks struct {
	Keys []struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func (v *oidcVerifier) fetchLocked() error {
	resp, err := v.client.Get(v.jwksURL)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	var set jwks
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return err
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := rsaPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	v.keys = keys
	v.fetchedAt = v.now()
	return nil
}

// rsaPublicKey builds a key from the JWK base64url modulus and exponent.
func rsaPublicKey(n, e string) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil {
		return nil, err
	}
	eb, err := base64.RawURLEncoding.DecodeString(e)
	if err != nil {
		return nil, err
	}
	exp := new(big.Int).SetBytes(eb)
	if !exp.IsInt64() || exp.Int64() > int64(^uint32(0)) {
		return nil, errors.New("exponent out of range")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(exp.Int64())}, nil
}
