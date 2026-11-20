// Package secrets is the encrypted file where the Poof! CLI keeps the
// credentials it uses on the operator's behalf (today: a GitHub token). The
// server never sees this file; it holds no credential at all.
//
// The key is derived from two ingredients mixed together, both optional: a
// secret in the POOF_KEY environment variable and a passphrase the operator
// types. An absent ingredient is the empty string; the mixing is the same
// either way, so there are no modes to reason about. Both empty is allowed
// and is called out as weak by the CLI: the file is then protected only by
// the pepper compiled into the binary, which is public.
//
// Non-secret settings live in the clear in the file's header so a command
// that needs none of the secrets never has to ask for the passphrase.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/crypto/argon2"
)

// pepper is mixed into the salt. It is public (the binary is), so it adds no
// strength against anyone who knows the file is Poof's; it only keeps a
// scraped file plus a scraped POOF_KEY from being decryptable without also
// knowing which program they belong to. Changing it orphans every existing
// file, so it only changes together with Version.
const pepper = "poof-credentials-v1:7d3a9c1e5b2f4860"

// Version of the file format.
const Version = 1

// Settings are the non-secret fields, stored in the clear.
type Settings struct {
	// Ask means a typed passphrase is one of the key ingredients.
	Ask bool `json:"ask"`
	// GH allows using the GitHub CLI's login. Nil means never asked.
	GH *bool `json:"gh,omitempty"`
	// GitHubTokenEnv names an environment variable holding a GitHub token.
	GitHubTokenEnv string `json:"github_token_env,omitempty"`
	// APITokenProfiles lists the profiles whose API token is sealed inside,
	// so `poof config` can say "stored" without decrypting. Profile names
	// are not secret: they are in poof.toml in the clear.
	APITokenProfiles []string `json:"api_token_profiles,omitempty"`
}

// HasAPIToken reports, from the clear header, whether a profile's token is sealed.
func (s Settings) HasAPIToken(profile string) bool {
	for _, p := range s.APITokenProfiles {
		if p == profile {
			return true
		}
	}
	return false
}

// Values are the secrets, stored encrypted.
type Values struct {
	GitHubToken string `json:"github_token,omitempty"`
	// APITokens holds the Poof API token of each client profile, keyed by
	// profile name ("" is the default profile).
	APITokens map[string]string `json:"api_tokens,omitempty"`
}

// IsEmpty reports whether there is nothing worth sealing.
func (v Values) IsEmpty() bool {
	return v.GitHubToken == "" && len(v.APITokens) == 0
}

// SetAPIToken stores (or, with "", removes) the API token of a profile and
// keeps the header's list of profiles in step.
func SetAPIToken(s *Settings, v *Values, profile, token string) {
	if token == "" {
		delete(v.APITokens, profile)
		if len(v.APITokens) == 0 {
			v.APITokens = nil
		}
	} else {
		if v.APITokens == nil {
			v.APITokens = map[string]string{}
		}
		v.APITokens[profile] = token
	}
	s.APITokenProfiles = s.APITokenProfiles[:0:0]
	for p := range v.APITokens {
		s.APITokenProfiles = append(s.APITokenProfiles, p)
	}
	sort.Strings(s.APITokenProfiles)
	if len(s.APITokenProfiles) == 0 {
		s.APITokenProfiles = nil
	}
}

// envelope is the on-disk shape.
type envelope struct {
	Version  int    `json:"v"`
	Settings        // flattened
	Salt     []byte `json:"salt,omitempty"`
	Nonce    []byte `json:"nonce,omitempty"`
	Data     []byte `json:"data,omitempty"`
}

// File is a loaded credentials file: its settings in the clear, its secrets
// still sealed until Open.
type File struct {
	Settings
	env envelope
}

// Ingredients are the two halves of the key.
type Ingredients struct {
	Env   string // $POOF_KEY
	Typed string // the passphrase, when Settings.Ask
}

// ErrWrongKey is returned by Open when the ingredients do not decrypt the file.
var ErrWrongKey = errors.New("credentials file: wrong passphrase or POOF_KEY")

// Load reads the file at path. A missing file is an empty File, not an error.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{}, nil
	}
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("credentials file %s: %w", path, err)
	}
	if env.Version != Version {
		return nil, fmt.Errorf("credentials file %s: unsupported version %d", path, env.Version)
	}
	return &File{Settings: env.Settings, env: env}, nil
}

// HasSecrets reports whether Open would have anything to decrypt.
func (f *File) HasSecrets() bool { return len(f.env.Data) > 0 }

// Open decrypts the secrets with ing. A file with no secrets opens with
// anything, including nothing.
func (f *File) Open(ing Ingredients) (Values, error) {
	if !f.HasSecrets() {
		return Values{}, nil
	}
	key := deriveKey(ing, f.env.Salt)
	plain, err := gcm(key).Open(nil, f.env.Nonce, f.env.Data, f.aad())
	if err != nil {
		return Values{}, ErrWrongKey
	}
	var v Values
	if err := json.Unmarshal(plain, &v); err != nil {
		return Values{}, fmt.Errorf("credentials file: %w", err)
	}
	return v, nil
}

// Save writes settings and values to path, encrypted with ing, atomically
// and readable by the owner only. Empty values are stored as no data, so a
// file holding only settings needs no key to read.
func Save(path string, settings Settings, values Values, ing Ingredients) error {
	env := envelope{Version: Version, Settings: settings}
	if !values.IsEmpty() {
		plain, err := json.Marshal(values)
		if err != nil {
			return err
		}
		env.Salt = randomBytes(16)
		env.Nonce = randomBytes(12)
		key := deriveKey(ing, env.Salt)
		env.Data = gcm(key).Seal(nil, env.Nonce, plain, (&File{env: env}).aad())
	}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// aad binds the clear-text settings to the ciphertext: flipping a setting in
// the file breaks decryption instead of silently taking effect.
func (f *File) aad() []byte {
	b, _ := json.Marshal(f.env.Settings)
	return append([]byte(fmt.Sprintf("v%d:", f.env.Version)), b...)
}

// deriveKey mixes the ingredients and stretches them. Each ingredient is
// length-prefixed before hashing so ("ab","c") and ("a","bc") differ; the
// pepper rides in the salt.
func deriveKey(ing Ingredients, salt []byte) []byte {
	h := sha256.New()
	for _, part := range []string{ing.Env, ing.Typed} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		h.Write(n[:])
		h.Write([]byte(part))
	}
	return argon2.IDKey(h.Sum(nil), append(append([]byte{}, salt...), pepper...), 3, 64*1024, 1, 32)
}

func gcm(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err) // key is always 32 bytes
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return g
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
