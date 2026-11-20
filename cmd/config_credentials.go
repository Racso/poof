package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/racso/poof/secrets"
)

// credentialKeys are the `poof config set|unset` keys that live in the
// encrypted credentials file rather than in poof.toml or on the server.
var credentialKeys = []string{"github-token", "github-token-env", "gh", "passphrase"}

func isCredentialKey(key string) bool {
	for _, k := range credentialKeys {
		if k == key {
			return true
		}
	}
	return false
}

func setCredentialKey(key, value string) {
	switch key {
	case "github-token":
		if value == "" {
			fatal("value required: poof config set github-token <token>  (or '-' to read it from stdin)")
		}
		if value == "-" {
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			value = strings.TrimSpace(line)
			if value == "" {
				fatal("no token on standard input")
			}
		}
		updateCredentials(func(_ *secrets.Settings, v *secrets.Values) { v.GitHubToken = value })
		fmt.Printf("github-token stored  →  %s\n", credentialsPath())
		warnWeakProtection(loadCredentials())

	case "github-token-env":
		if value == "" {
			fatal("value required: poof config set github-token-env <VARIABLE>")
		}
		updateCredentials(func(s *secrets.Settings, _ *secrets.Values) { s.GitHubTokenEnv = value })
		fmt.Printf("github-token-env = %s\n", value)

	case "gh":
		var allow bool
		switch strings.ToLower(value) {
		case "yes", "y", "true", "on":
			allow = true
		case "no", "n", "false", "off":
			allow = false
		default:
			fatal("value required: poof config set gh yes|no")
		}
		updateCredentials(func(s *secrets.Settings, _ *secrets.Values) { s.GH = &allow })
		fmt.Printf("gh = %v\n", allow)

	case "passphrase":
		if value != "" {
			fatal("the passphrase is prompted, not passed on the command line (it would land in your shell history)")
		}
		// Open with the current ingredients, then re-seal with the new ones.
		f := loadCredentials()
		values, err := f.Open(ingredients(f.Ask, false))
		if err != nil {
			fatal("%v", err)
		}
		settings := f.Settings
		settings.Ask = true
		saveCredentials(settings, values, ingredients(true, true))
		fmt.Printf("passphrase set  →  %s\n", credentialsPath())
		fmt.Println("  it is asked whenever the stored token is needed; 'poof config unset passphrase' removes it")
	}
}

func unsetCredentialKey(key string) {
	switch key {
	case "github-token":
		updateCredentials(func(_ *secrets.Settings, v *secrets.Values) { v.GitHubToken = "" })
	case "github-token-env":
		updateCredentials(func(s *secrets.Settings, _ *secrets.Values) { s.GitHubTokenEnv = "" })
	case "gh":
		updateCredentials(func(s *secrets.Settings, _ *secrets.Values) { s.GH = nil })
	case "passphrase":
		f := loadCredentials()
		values, err := f.Open(ingredients(f.Ask, false))
		if err != nil {
			fatal("%v", err)
		}
		settings := f.Settings
		settings.Ask = false
		saveCredentials(settings, values, ingredients(false, false))
	}
	fmt.Printf("%s cleared\n", key)
	warnWeakProtection(loadCredentials())
}

// showCredentials is the credentials block of `poof config`. It never
// decrypts anything: whether a token is stored is visible from the header.
func showCredentials() {
	f := loadCredentials()
	fmt.Printf("credentials    %s\n", credentialsPath())
	gh := "(not decided; asked on first use)"
	if f.GH != nil {
		gh = map[bool]string{true: "yes", false: "no"}[*f.GH]
	}
	fmt.Printf("  %-20s  %s\n", "gh", gh)
	token := "(not set)"
	if f.HasSecrets() {
		token = "(stored, encrypted)"
	}
	fmt.Printf("  %-20s  %s\n", "github-token", token)
	fmt.Printf("  %-20s  %s\n", "github-token-env", displayValue(f.GitHubTokenEnv))
	var layers []string
	if os.Getenv(keyEnvVar) != "" {
		layers = append(layers, "$"+keyEnvVar)
	}
	if f.Ask {
		layers = append(layers, "passphrase")
	}
	protection := strings.Join(layers, " + ")
	switch {
	case !f.HasSecrets():
		protection = "(nothing stored)"
	case protection == "":
		protection = "NONE (weak): set $" + keyEnvVar + " or 'poof config set passphrase'"
	}
	fmt.Printf("  %-20s  %s\n", "protection", protection)
}
