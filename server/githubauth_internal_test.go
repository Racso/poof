package server

import (
	"strings"
	"testing"
	"time"

	"github.com/racso/poof/config"
)

func TestLoginBlobSealedToAPIToken(t *testing.T) {
	a := &Server{cfg: &config.ServerConfig{Token: "token-a"}}
	b := &Server{cfg: &config.ServerConfig{Token: "token-b"}}
	blob := &loginBlob{Login: "Racso", AccessToken: "ghu_secret", ExpiresAt: time.Now(), RefreshToken: "ghr_secret"}

	sealed, err := a.sealLogin(blob)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "ghu_secret") || strings.Contains(sealed, "Racso") {
		t.Errorf("sealed blob leaks plaintext: %s", sealed)
	}
	got, err := a.openLogin(sealed)
	if err != nil || got.AccessToken != "ghu_secret" || got.Login != "Racso" {
		t.Fatalf("open with the same token: %+v, %v", got, err)
	}
	if _, err := b.openLogin(sealed); err == nil || !strings.Contains(err.Error(), "poof login") {
		t.Errorf("another API token must not open the blob, and the error should say what to do: %v", err)
	}
	if _, err := a.openLogin("not base64!"); err == nil {
		t.Error("garbage should fail")
	}
}
