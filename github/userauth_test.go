package github

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeGitHub answers the device-flow endpoints with scripted bodies.
func fakeGitHub(t *testing.T, routes map[string]func(r *http.Request) (int, string)) *DeviceFlow {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		code, body := h(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	d := NewDeviceFlow("client-1")
	d.base, d.apiBase = srv.URL, srv.URL
	d.now = func() time.Time { return time.Unix(1_000_000, 0) }
	return d
}

func TestDeviceFlowStart(t *testing.T) {
	d := fakeGitHub(t, map[string]func(*http.Request) (int, string){
		"/login/device/code": func(r *http.Request) (int, string) {
			if r.Form.Get("client_id") != "client-1" {
				t.Errorf("client_id = %q", r.Form.Get("client_id"))
			}
			return 200, `{"device_code":"dc","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":899,"interval":5}`
		},
	})
	code, err := d.Start()
	if err != nil {
		t.Fatal(err)
	}
	if code.UserCode != "ABCD-1234" || code.DeviceCode != "dc" || code.Interval != 5 {
		t.Errorf("unexpected code: %+v", code)
	}
}

func TestDeviceFlowPollOutcomes(t *testing.T) {
	answers := []string{
		`{"error":"authorization_pending","error_description":"..."}`,
		`{"error":"slow_down"}`,
		`{"error":"expired_token"}`,
		`{"error":"access_denied"}`,
		`{"access_token":"ghu_x","expires_in":28800,"refresh_token":"ghr_y","refresh_token_expires_in":15638400,"token_type":"bearer","scope":""}`,
	}
	i := 0
	d := fakeGitHub(t, map[string]func(*http.Request) (int, string){
		"/login/oauth/access_token": func(r *http.Request) (int, string) {
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.Form.Get("device_code") != "dc" {
				t.Errorf("unexpected form: %v", r.Form)
			}
			a := answers[i]
			i++
			return 200, a
		},
	})
	for _, want := range []error{ErrAuthorizationPending, ErrSlowDown, ErrDeviceCodeExpired, ErrAccessDenied} {
		if _, err := d.Poll("dc"); !errors.Is(err, want) {
			t.Errorf("got %v, want %v", err, want)
		}
	}
	tok, err := d.Poll("dc")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "ghu_x" || tok.RefreshToken != "ghr_y" {
		t.Errorf("unexpected token: %+v", tok)
	}
	if got := tok.ExpiresAt.Sub(d.now()); got != 8*time.Hour {
		t.Errorf("access expiry = %v", got)
	}
	if got := tok.RefreshExpiresAt.Sub(d.now()); got != 15638400*time.Second {
		t.Errorf("refresh expiry = %v", got)
	}
}

func TestDeviceFlowRefreshAndLogin(t *testing.T) {
	d := fakeGitHub(t, map[string]func(*http.Request) (int, string){
		"/login/oauth/access_token": func(r *http.Request) (int, string) {
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "ghr_old" {
				t.Errorf("unexpected form: %v", r.Form)
			}
			return 200, `{"access_token":"ghu_new","expires_in":28800,"refresh_token":"ghr_new","refresh_token_expires_in":100}`
		},
		"/user": func(r *http.Request) (int, string) {
			if r.Header.Get("Authorization") != "Bearer ghu_new" {
				return 401, `{"message":"Bad credentials"}`
			}
			return 200, `{"login":"Racso"}`
		},
	})
	tok, err := d.Refresh("ghr_old")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "ghu_new" || tok.RefreshToken != "ghr_new" {
		t.Errorf("unexpected token: %+v", tok)
	}
	login, err := d.Login(tok.AccessToken)
	if err != nil || login != "Racso" {
		t.Errorf("login = %q, %v", login, err)
	}
	if _, err := d.Login("ghu_stale"); err == nil {
		t.Error("stale token should fail")
	}
}

func TestDeviceFlowUnknownErrorIsReported(t *testing.T) {
	d := fakeGitHub(t, map[string]func(*http.Request) (int, string){
		"/login/oauth/access_token": func(r *http.Request) (int, string) {
			return 200, `{"error":"incorrect_client_credentials","error_description":"The client_id is wrong"}`
		},
	})
	_, err := d.Poll("dc")
	if err == nil || errors.Is(err, ErrAuthorizationPending) {
		t.Fatalf("expected a reported error, got %v", err)
	}
}
