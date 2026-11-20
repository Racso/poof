package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/racso/poof/config"
	"github.com/racso/poof/secrets"
	"golang.org/x/term"
)

// keyEnvVar holds the machine half of the credentials key.
const keyEnvVar = "POOF_KEY"

// passphraseStdin is the global --passphrase-stdin flag: read the typed half
// of the key from standard input instead of the terminal, for automation.
var passphraseStdin bool

// credentialsPath is the encrypted credentials file, next to the client
// config. It is shared by every profile: a GitHub credential is the
// operator's, not a server's.
func credentialsPath() string {
	return filepath.Join(filepath.Dir(config.ClientConfigPath()), "credentials.json")
}

// loadCredentials reads the file's clear-text settings. Nothing is decrypted.
func loadCredentials() *secrets.File {
	f, err := secrets.Load(credentialsPath())
	if err != nil {
		fatal("%v", err)
	}
	return f
}

// ingredients gathers the key: $POOF_KEY, and the passphrase when the file
// asks for one (from --passphrase-stdin or the terminal). confirm makes the
// terminal prompt ask twice, for setting a new passphrase.
func ingredients(ask bool, confirm bool) secrets.Ingredients {
	ing := secrets.Ingredients{Env: os.Getenv(keyEnvVar)}
	if !ask {
		return ing
	}
	if passphraseStdin {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		ing.Typed = strings.TrimRight(line, "\r\n")
		return ing
	}
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		fatal("the credentials file needs its passphrase and there is no terminal to ask on: pass --passphrase-stdin")
	}
	ing.Typed = promptSecret("Credentials passphrase: ")
	if confirm {
		if again := promptSecret("Repeat passphrase: "); again != ing.Typed {
			fatal("passphrases do not match")
		}
	}
	return ing
}

func promptSecret(prompt string) string {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		fatal("reading passphrase: %v", err)
	}
	return string(b)
}

// warnWeakProtection says so when the key has no ingredient at all: the
// file is then readable by anyone who has it and knows it is Poof's.
func warnWeakProtection(f *secrets.File) {
	if !f.HasSecrets() || f.Ask || os.Getenv(keyEnvVar) != "" {
		return
	}
	fmt.Fprintf(os.Stderr, "WEAK: %s is not protected: set %s in this machine's environment, or 'poof config set passphrase'\n", credentialsPath(), keyEnvVar)
}

// openCredentials decrypts the stored secrets, asking for the passphrase if
// the file wants one.
func openCredentials(f *secrets.File) secrets.Values {
	warnWeakProtection(f)
	v, err := f.Open(ingredients(f.Ask, false))
	if err != nil {
		fatalWrongKey(err)
	}
	return v
}

// fatalWrongKey explains the one recoverable cause of a failed decryption
// that is not a typo: the machine half of the key changed under the file.
func fatalWrongKey(err error) {
	if errors.Is(err, secrets.ErrWrongKey) {
		fatal("%v\n       If %s changed on this machine, the file was sealed with the old value: restore it, 'poof config unset github-token', set the new value and store the token again", err, keyEnvVar)
	}
	fatal("%v", err)
}

// saveCredentials rewrites the file with new settings and values. Changing
// the passphrase or the env secret is done by saving with new ingredients.
func saveCredentials(settings secrets.Settings, values secrets.Values, ing secrets.Ingredients) {
	if err := secrets.Save(credentialsPath(), settings, values, ing); err != nil {
		fatal("writing %s: %v", credentialsPath(), err)
	}
}

// updateCredentials opens the file, lets fn change settings and values, and
// saves them back under the same ingredients (the passphrase is asked once
// if the file has one).
func updateCredentials(fn func(*secrets.Settings, *secrets.Values)) {
	f := loadCredentials()
	ing := ingredients(f.Ask, false)
	values, err := f.Open(ing)
	if err != nil {
		fatalWrongKey(err)
	}
	settings := f.Settings
	fn(&settings, &values)
	saveCredentials(settings, values, ing)
}

// githubCredential finds a token on this machine able to act on GitHub, and
// says where it came from. In order: the GitHub CLI's login when allowed, the
// token stored in the credentials file, the environment variable the file
// names, then $GH_TOKEN and $GITHUB_TOKEN. Empty when there is none.
func githubCredential() (token, source string) {
	f := loadCredentials()
	if ghAllowed(f) {
		if tok := ghToken(); tok != "" {
			return tok, "gh"
		}
	}
	if f.HasSecrets() {
		if v := openCredentials(f); v.GitHubToken != "" {
			return v.GitHubToken, "stored token"
		}
	}
	if f.GitHubTokenEnv != "" {
		if tok := os.Getenv(f.GitHubTokenEnv); tok != "" {
			return tok, "$" + f.GitHubTokenEnv
		}
	}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if tok := os.Getenv(name); tok != "" {
			return tok, "$" + name
		}
	}
	return "", ""
}

// ghToken is what `gh auth token` has for github.com, or "".
func ghToken() string {
	path, err := exec.LookPath("gh")
	if err != nil {
		return ""
	}
	out, err := exec.Command(path, "auth", "token", "--hostname", "github.com").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ghAllowed answers whether the GitHub CLI's login may be used. Using
// someone's gh login is opt-in: the first time it would matter and a
// terminal is present, the operator is asked once and the answer is kept.
// Without a terminal the answer is no, and the links are printed instead.
func ghAllowed(f *secrets.File) bool {
	if f.GH != nil {
		return *f.GH
	}
	if ghToken() == "" || !isatty.IsTerminal(os.Stdin.Fd()) {
		return false
	}
	fmt.Fprint(os.Stderr, "Use your GitHub CLI login (gh) to update workflow files in your repositories? [Y/n] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	yes := !strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "n")
	updateCredentials(func(s *secrets.Settings, _ *secrets.Values) { s.GH = &yes })
	fmt.Fprintf(os.Stderr, "  remembered in %s ('poof config set gh yes|no' changes it)\n", credentialsPath())
	return yes
}
