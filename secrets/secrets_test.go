package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "credentials.json")
}

func TestRoundTrip(t *testing.T) {
	path := tempPath(t)
	yes := true
	settings := Settings{Ask: true, GH: &yes, GitHubTokenEnv: "GH_TOKEN"}
	ing := Ingredients{Env: "machine-secret", Typed: "correct horse"}
	if err := Save(path, settings, Values{GitHubToken: "ghp_secret"}, ing); err != nil {
		t.Fatalf("save: %v", err)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !f.Ask || f.GH == nil || !*f.GH || f.GitHubTokenEnv != "GH_TOKEN" {
		t.Errorf("settings not preserved in the clear: %+v", f.Settings)
	}
	if !f.HasSecrets() {
		t.Fatal("expected sealed secrets")
	}
	v, err := f.Open(ing)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if v.GitHubToken != "ghp_secret" {
		t.Errorf("token: got %q", v.GitHubToken)
	}

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "ghp_secret") {
		t.Error("token stored in the clear")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0600 {
		t.Errorf("mode: got %o", fi.Mode().Perm())
	}
}

func TestWrongIngredientsFail(t *testing.T) {
	path := tempPath(t)
	ing := Ingredients{Env: "e", Typed: "t"}
	if err := Save(path, Settings{Ask: true}, Values{GitHubToken: "x"}, ing); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(path)
	for _, bad := range []Ingredients{{Env: "e"}, {Typed: "t"}, {Env: "E", Typed: "t"}, {}} {
		if _, err := f.Open(bad); !errors.Is(err, ErrWrongKey) {
			t.Errorf("ingredients %+v: expected ErrWrongKey, got %v", bad, err)
		}
	}
}

func TestIngredientsAreLengthPrefixed(t *testing.T) {
	// Plain concatenation would make these two the same key.
	salt := []byte("0123456789abcdef")
	a := deriveKey(Ingredients{Env: "ab", Typed: "c"}, salt)
	b := deriveKey(Ingredients{Env: "a", Typed: "bc"}, salt)
	if string(a) == string(b) {
		t.Error("('ab','c') and ('a','bc') derive the same key")
	}
}

func TestBothIngredientsEmptyStillRoundTrips(t *testing.T) {
	path := tempPath(t)
	if err := Save(path, Settings{}, Values{GitHubToken: "x"}, Ingredients{}); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(path)
	v, err := f.Open(Ingredients{})
	if err != nil || v.GitHubToken != "x" {
		t.Errorf("open with empty ingredients: %v %+v", err, v)
	}
}

func TestSettingsAreBoundToTheCiphertext(t *testing.T) {
	path := tempPath(t)
	ing := Ingredients{Env: "e"}
	if err := Save(path, Settings{Ask: false}, Values{GitHubToken: "x"}, ing); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	tampered := strings.Replace(string(raw), `"ask": false`, `"ask": true`, 1)
	if tampered == string(raw) {
		t.Fatal("test setup: ask field not found")
	}
	os.WriteFile(path, []byte(tampered), 0600)
	f, _ := Load(path)
	if _, err := f.Open(ing); !errors.Is(err, ErrWrongKey) {
		t.Errorf("edited header must break decryption, got %v", err)
	}
}

func TestSettingsOnlyFileNeedsNoKey(t *testing.T) {
	path := tempPath(t)
	no := false
	if err := Save(path, Settings{GH: &no}, Values{}, Ingredients{Env: "whatever"}); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(path)
	if f.HasSecrets() {
		t.Error("no values were given, nothing should be sealed")
	}
	if _, err := f.Open(Ingredients{}); err != nil {
		t.Errorf("open: %v", err)
	}
	if f.GH == nil || *f.GH {
		t.Errorf("gh setting lost: %+v", f.Settings)
	}
}

func TestAPITokensPerProfile(t *testing.T) {
	path := tempPath(t)
	ing := Ingredients{Env: "e"}
	var s Settings
	var v Values
	SetAPIToken(&s, &v, "", "default-tok")
	SetAPIToken(&s, &v, "work", "work-tok")
	if err := Save(path, s, v, ing); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(path)
	if !f.HasAPIToken("") || !f.HasAPIToken("work") || f.HasAPIToken("other") {
		t.Errorf("header profile list wrong: %v", f.APITokenProfiles)
	}
	got, err := f.Open(ing)
	if err != nil {
		t.Fatal(err)
	}
	if got.APITokens[""] != "default-tok" || got.APITokens["work"] != "work-tok" {
		t.Errorf("tokens: %+v", got.APITokens)
	}
	if _, err := f.Open(Ingredients{Env: "x"}); !errors.Is(err, ErrWrongKey) {
		t.Errorf("wrong key must fail, got %v", err)
	}

	s = f.Settings
	SetAPIToken(&s, &got, "work", "")
	SetAPIToken(&s, &got, "", "")
	if !got.IsEmpty() || got.APITokens != nil || s.APITokenProfiles != nil {
		t.Errorf("removing every token should leave Values and the header empty: %+v %+v", got, s)
	}
	if err := Save(path, s, got, ing); err != nil {
		t.Fatal(err)
	}
	f, _ = Load(path)
	if f.HasSecrets() {
		t.Error("an emptied Values must not leave ciphertext behind")
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || f.HasSecrets() || f.Ask {
		t.Errorf("missing file: %v %+v", err, f)
	}
}
