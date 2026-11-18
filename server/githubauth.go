package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/racso/poof/defaults"
)

// GitHub access for the operations that write to a repository (`poof add`,
// `refresh`, `clone`, `remove`). Deploys do not come through here: they
// authenticate with OIDC and pull with the job's own token (see oidc.go).
//
// Two credentials are supported, in this order of preference:
//
//   - A device-flow login (`poof login`): a user-to-server token of the public
//     Poof GitHub App, bounded by the App's permissions and by the repos the
//     operator ticked when installing it. 8-hour access token refreshed
//     silently from a refresh token. Stored encrypted; see loginBlob.
//   - A fine-grained personal access token (`poof config set github-token`),
//     for operators who prefer one. Used as is.

// Setting keys.
const (
	settingGitHubToken    = "github-token"
	settingGitHubClientID = "github-client-id"
	// settingGitHubLogin holds the encrypted login blob.
	settingGitHubLogin = "github-login"
)

// Credential sources, as reported by GET /auth/github and GitHubAuth.Source.
const (
	authSourceLogin = "login"
	authSourceToken = "token"
	authSourceNone  = "none"
)

// loginRefreshLead is how close to expiry an access token is refreshed.
const loginRefreshLead = 10 * time.Minute

// ErrAuthorizationPending is what a GitHubProvider returns from DevicePoll
// while the operator has not approved the code yet.
var ErrAuthorizationPending = errors.New("authorization pending")

// loginBlob is the persisted login: the token pair, who it acts as, and when
// each half expires.
type loginBlob struct {
	Login            string    `json:"login"`
	AccessToken      string    `json:"access_token"`
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

func (s *Server) githubClientID() string {
	if v, _ := s.store.GetSetting(settingGitHubClientID); v != "" {
		return v
	}
	return defaults.GitHubClientID
}

// --- Encrypted storage ---

// loginKey derives the AES key sealing the login blob from the server's API
// token. The database alone (a backup, a copied file) then yields nothing;
// poof.toml, mode 0600, is needed too. Rotating the API token invalidates
// the blob, which costs one `poof login`.
func (s *Server) loginKey() []byte {
	sum := sha256.Sum256([]byte("poof github login\x00" + s.cfg.Token))
	return sum[:]
}

func (s *Server) sealLogin(b *loginBlob) (string, error) {
	plain, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.loginKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

func (s *Server) openLogin(sealed string) (*loginBlob, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.loginKey())
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("login blob too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return nil, errors.New("cannot decrypt the stored GitHub login (API token changed?): run 'poof login' again")
	}
	var b loginBlob
	if err := json.Unmarshal(plain, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// loadLogin returns the stored login, or nil when none is stored.
func (s *Server) loadLogin() (*loginBlob, error) {
	sealed, _ := s.store.GetSetting(settingGitHubLogin)
	if sealed == "" {
		return nil, nil
	}
	return s.openLogin(sealed)
}

func (s *Server) saveLogin(b *loginBlob) error {
	sealed, err := s.sealLogin(b)
	if err != nil {
		return err
	}
	return s.store.SetSetting(settingGitHubLogin, sealed)
}

// --- Resolving a usable token ---

// loginAccessToken returns a valid access token from the stored login,
// refreshing it when it is about to expire. GitHub revokes the previous
// access token when a refresh succeeds, so the new pair is persisted before
// it is handed out; a failed save keeps the old pair in use.
func (s *Server) loginAccessToken() (string, error) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	b, err := s.loadLogin()
	if err != nil || b == nil {
		return "", err
	}
	now := time.Now()
	if now.Before(b.ExpiresAt.Add(-loginRefreshLead)) {
		return b.AccessToken, nil
	}
	if b.RefreshToken == "" || (!b.RefreshExpiresAt.IsZero() && now.After(b.RefreshExpiresAt)) {
		return "", errors.New("GitHub login expired: run 'poof login'")
	}
	tok, err := s.github.RefreshUserToken(s.githubClientID(), b.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("refresh GitHub login: %w (run 'poof login' if this persists)", err)
	}
	b.AccessToken, b.ExpiresAt = tok.AccessToken, tok.ExpiresAt
	b.RefreshToken, b.RefreshExpiresAt = tok.RefreshToken, tok.RefreshExpiresAt
	if err := s.saveLogin(b); err != nil {
		return "", fmt.Errorf("persist refreshed GitHub login: %w", err)
	}
	log.Printf("github login refreshed for %s (valid until %s)", b.Login, b.ExpiresAt.Format(time.RFC3339))
	return b.AccessToken, nil
}

// githubAuth resolves the credential for repository writes: the login when
// one is stored and usable, else the PAT, else nothing. Read on every use so
// `poof login` and `poof config set` take effect without a restart.
func (s *Server) githubAuth() GitHubAuth {
	token, err := s.loginAccessToken()
	if err != nil {
		log.Printf("warning: %v", err)
	}
	if token != "" {
		return GitHubAuth{Token: token, Source: authSourceLogin}
	}
	if pat, _ := s.store.GetSetting(settingGitHubToken); pat != "" {
		return GitHubAuth{Token: pat, Source: authSourceToken}
	}
	return GitHubAuth{}
}

// noGitHubAuthHint is the error body when a write needs GitHub and none is set.
const noGitHubAuthHint = "no GitHub access configured on server: run 'poof login' (or 'poof config set github-token <fine-grained PAT>')"

// --- Device flow endpoints ---

// pendingLogin is a device flow in progress, polled by a goroutine.
type pendingLogin struct {
	UserCode  string
	ExpiresAt time.Time
	Err       error
	Done      bool
}

// startDeviceLogin begins a device flow and returns what the operator must
// type where. The server polls GitHub in the background until the code is
// approved, refused or expires; GET /auth/github reports the outcome.
func (s *Server) startDeviceLogin(w http.ResponseWriter, r *http.Request) {
	clientID := s.githubClientID()
	code, err := s.github.DeviceStart(clientID)
	if err != nil {
		jsonError(w, fmt.Sprintf("start GitHub device flow: %v", err), http.StatusBadGateway)
		return
	}
	expires := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	s.loginMu.Lock()
	s.pending = &pendingLogin{UserCode: code.UserCode, ExpiresAt: expires}
	pending := s.pending
	s.loginMu.Unlock()

	go s.pollDeviceLogin(clientID, code, pending)

	jsonOK(w, map[string]interface{}{
		"user_code":        code.UserCode,
		"verification_uri": code.VerificationURI,
		"expires_in":       code.ExpiresIn,
		"interval":         code.Interval,
		"app_url":          defaults.GitHubAppURL,
	})
}

func (s *Server) pollDeviceLogin(clientID string, code DeviceCode, pending *pendingLogin) {
	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	finish := func(err error) {
		s.loginMu.Lock()
		defer s.loginMu.Unlock()
		pending.Done, pending.Err = true, err
	}
	for {
		tok, err := s.github.DevicePoll(clientID, code.DeviceCode)
		switch {
		case errors.Is(err, ErrAuthorizationPending):
			if time.Now().After(pending.ExpiresAt) {
				finish(errors.New("the code expired before it was approved"))
				return
			}
			time.Sleep(interval)
			continue
		case err != nil:
			finish(err)
			return
		}
		login, err := s.github.UserLogin(tok.AccessToken)
		if err != nil {
			finish(fmt.Errorf("look up the authorized user: %w", err))
			return
		}
		blob := &loginBlob{
			Login: login, AccessToken: tok.AccessToken, ExpiresAt: tok.ExpiresAt,
			RefreshToken: tok.RefreshToken, RefreshExpiresAt: tok.RefreshExpiresAt,
		}
		s.loginMu.Lock()
		err = s.saveLogin(blob)
		s.loginMu.Unlock()
		if err != nil {
			finish(fmt.Errorf("store login: %w", err))
			return
		}
		// The login is the natural default owner for new projects.
		if s.settingGitHubUser() == "" {
			_ = s.store.SetSetting("github-user", login)
		}
		log.Printf("github login completed as %s", login)
		finish(nil)
		return
	}
}

// getGitHubAuth reports which credential is in place, without revealing it.
func (s *Server) getGitHubAuth(w http.ResponseWriter, r *http.Request) {
	out := map[string]interface{}{"source": authSourceNone, "client_id": s.githubClientID()}

	s.loginMu.Lock()
	if p := s.pending; p != nil && !p.Done {
		out["pending"] = true
		out["user_code"] = p.UserCode
	} else if p != nil && p.Err != nil {
		out["pending_error"] = p.Err.Error()
	}
	s.loginMu.Unlock()

	b, err := s.loadLogin()
	switch {
	case err != nil:
		out["login_error"] = err.Error()
	case b != nil:
		out["source"] = authSourceLogin
		out["login"] = b.Login
		out["expires_at"] = b.ExpiresAt
		out["refresh_expires_at"] = b.RefreshExpiresAt
	}
	if pat, _ := s.store.GetSetting(settingGitHubToken); pat != "" {
		out["token"] = maskSecret(pat)
		if out["source"] == authSourceNone {
			out["source"] = authSourceToken
		}
	}
	jsonOK(w, out)
}

// deleteGitHubAuth forgets the stored login. GitHub keeps the authorization
// until the operator revokes it there, but this server can no longer use it.
func (s *Server) deleteGitHubAuth(w http.ResponseWriter, r *http.Request) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if err := s.store.DeleteSetting(settingGitHubLogin); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.pending = nil
	log.Printf("github login cleared")
	jsonOK(w, map[string]string{"status": "cleared"})
}
