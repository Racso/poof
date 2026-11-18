package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Device flow against the public Poof GitHub App. A server authenticates to
// GitHub as the operator who approves a one-time code in their browser; what
// comes back is a user-to-server token bounded by the App's permissions and
// by the repositories the operator chose when installing the App. Only the
// App's client id is involved, which is public: no private key, no client
// secret, nothing a self-hoster has to create.
//
// Tokens expire after 8 hours and come with a refresh token (about 6 months).
// GitHub revokes the old access token the moment a refresh succeeds, so the
// new pair must be persisted before the old one is forgotten.

const githubBase = "https://github.com"

// DeviceCode is what the operator is asked to type where.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// UserToken is an access token with its refresh token and both expiries.
type UserToken struct {
	AccessToken      string
	ExpiresAt        time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// Device-flow outcomes a caller handles rather than reports.
var (
	// ErrAuthorizationPending: the operator has not approved the code yet.
	ErrAuthorizationPending = errors.New("authorization pending")
	// ErrSlowDown: polled too fast; wait the extra interval before retrying.
	ErrSlowDown = errors.New("slow down")
	// ErrDeviceCodeExpired: the code lapsed before approval; start over.
	ErrDeviceCodeExpired = errors.New("device code expired")
	// ErrAccessDenied: the operator refused.
	ErrAccessDenied = errors.New("access denied")
)

// DeviceFlow drives the OAuth device flow for one App client id.
type DeviceFlow struct {
	ClientID string
	base     string
	apiBase  string
	client   *http.Client
	now      func() time.Time
}

func NewDeviceFlow(clientID string) *DeviceFlow {
	return &DeviceFlow{
		ClientID: clientID,
		base:     githubBase,
		apiBase:  apiBase,
		client:   &http.Client{Timeout: 15 * time.Second},
		now:      time.Now,
	}
}

// Start requests a device code. The operator enters UserCode at
// VerificationURI; the caller then polls with DeviceCode.
func (d *DeviceFlow) Start() (DeviceCode, error) {
	var code DeviceCode
	err := d.post("/login/device/code", url.Values{"client_id": {d.ClientID}}, &code)
	if err != nil {
		return DeviceCode{}, err
	}
	if code.DeviceCode == "" || code.UserCode == "" {
		return DeviceCode{}, errors.New("GitHub returned no device code")
	}
	if code.Interval <= 0 {
		code.Interval = 5
	}
	return code, nil
}

// Poll makes one attempt to exchange deviceCode for a token. Until the
// operator approves it returns ErrAuthorizationPending (or ErrSlowDown).
func (d *DeviceFlow) Poll(deviceCode string) (UserToken, error) {
	return d.token(url.Values{
		"client_id":   {d.ClientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	})
}

// Refresh exchanges a refresh token for a new token pair. The previous access
// token is revoked by GitHub as a side effect.
func (d *DeviceFlow) Refresh(refreshToken string) (UserToken, error) {
	return d.token(url.Values{
		"client_id":     {d.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	})
}

// Login returns the GitHub login the access token acts as.
func (d *DeviceFlow) Login(accessToken string) (string, error) {
	req, err := http.NewRequest("GET", d.apiBase+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GitHub API %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", err
	}
	return u.Login, nil
}

type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Error                 string `json:"error"`
	ErrorDescription      string `json:"error_description"`
}

func (d *DeviceFlow) token(form url.Values) (UserToken, error) {
	var tr tokenResponse
	if err := d.post("/login/oauth/access_token", form, &tr); err != nil {
		return UserToken{}, err
	}
	switch tr.Error {
	case "":
	case "authorization_pending":
		return UserToken{}, ErrAuthorizationPending
	case "slow_down":
		return UserToken{}, ErrSlowDown
	case "expired_token":
		return UserToken{}, ErrDeviceCodeExpired
	case "access_denied":
		return UserToken{}, ErrAccessDenied
	default:
		return UserToken{}, fmt.Errorf("GitHub: %s: %s", tr.Error, tr.ErrorDescription)
	}
	if tr.AccessToken == "" {
		return UserToken{}, errors.New("GitHub returned no access token")
	}
	now := d.now()
	tok := UserToken{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	if tr.ExpiresIn > 0 {
		tok.ExpiresAt = now.Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	if tr.RefreshTokenExpiresIn > 0 {
		tok.RefreshExpiresAt = now.Add(time.Duration(tr.RefreshTokenExpiresIn) * time.Second)
	}
	return tok, nil
}

// post sends a form to github.com and decodes the JSON answer. GitHub reports
// device-flow conditions as 200s with an `error` field, so HTTP status alone
// is not the verdict; the caller inspects the decoded body.
func (d *DeviceFlow) post(path string, form url.Values, out interface{}) error {
	req, err := http.NewRequest("POST", d.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 {
		return fmt.Errorf("GitHub %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GitHub %s: unexpected response", resp.Status)
	}
	return nil
}
