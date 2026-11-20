package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/racso/poof/config"
	"github.com/racso/poof/secrets"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or set configuration",
	Args:  cobra.NoArgs,
	Run:   runConfigShow,
}

// serverConfigKeys are the settings stored on the server, in display order.
var serverConfigKeys = []string{"domain", "github-user"}

var configSetCmd = &cobra.Command{
	Use:   "set <key> [value]",
	Short: "Set a configuration value",
	Long: `Set a configuration value.

Local keys:
  server               URL of the Poof! server. Omit value on the server machine itself.
                       Written to this machine's config file (poof.toml).
  token                API token for authenticating with the server. Kept in the
                       encrypted credentials file, per profile; pass '-' to read it
                       from standard input. $POOF_TOKEN overrides it. A token still
                       in poof.toml from older versions keeps working, with a note.

Local credential keys (kept encrypted in credentials.json; see 'poof ci --help'):
  github-token         GitHub token used to write workflow files and dispatch runs.
                       Pass '-' to read it from standard input.
  github-token-env     Name of an environment variable holding such a token.
  gh                   yes|no: allow using the GitHub CLI's login.
  passphrase           Ask for a passphrase (typed, never stored) whenever the
                       stored token is needed. Omit the value: it is prompted.

Server keys (sent to the remote server — requires server and token to be set):
  domain               Default domain for new projects (e.g. mydomain.com).
  github-user          GitHub account that owns the repositories and images by default
                       (--repo and --image fall back to it). Not a credential: the server
                       holds none; see 'poof ci --help'.`,
	Args: cobra.RangeArgs(1, 2),
	Run:  runConfigSet,
}

var configUnsetCmd = &cobra.Command{
	Use:   "unset <key>",
	Short: "Clear a server configuration value",
	Long:  `Clear a server configuration value.`,
	Args:  cobra.ExactArgs(1),
	Run:   runConfigUnset,
}

func init() {
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configUnsetCmd)
	rootCmd.AddCommand(configCmd)
}

func runConfigShow(cmd *cobra.Command, args []string) {
	path := config.ClientConfigPath()
	fmt.Printf("client config  %s\n", path)
	fmt.Printf("  %-20s  %s\n", "server", displayValue(cfg.Server))
	fmt.Printf("  %-20s  %s\n", "token", describeToken())

	fmt.Println()
	showCredentials()

	if cfg.Server == "" {
		fmt.Println()
		fmt.Println("server settings  (not configured — run: poof config set server <url>)")
		return
	}

	fmt.Println()
	serverLabel := cfg.Server
	if cfg.IsLocal() {
		serverLabel = "local"
	}

	var settings map[string]string
	if err := apiGet("/config", &settings); err != nil {
		fmt.Printf("server settings  (%s — unreachable: %v)\n", serverLabel, err)
		return
	}

	fmt.Printf("server settings  (%s)\n", serverLabel)
	for _, key := range serverConfigKeys {
		fmt.Printf("  %-20s  %s\n", key, displayValue(settings[key]))
	}
}

func isServerConfigKey(key string) bool {
	for _, k := range serverConfigKeys {
		if k == key {
			return true
		}
	}
	return false
}

func requireServerConnection() {
	if cfg.Server == "" {
		fatal("server not configured — run: poof config set server <url> first")
	}
	if tok, _ := resolveToken(); tok == "" {
		fatal("token not configured — run: poof config set token <token> first")
	}
}

// describeToken is the `poof config` line for the API token: where it is,
// never what it is. It does not decrypt anything.
func describeToken() string {
	if os.Getenv("POOF_TOKEN") != "" {
		return "$POOF_TOKEN"
	}
	inFile := config.ClientFileHasToken(config.ClientConfigPath(), activeProfile())
	if loadCredentials().HasAPIToken(activeProfile()) {
		if inFile {
			return "(stored, encrypted; a clear-text copy may remain in poof.toml: 'poof config set token <token>' removes it)"
		}
		return "(stored, encrypted)"
	}
	if inFile {
		return "(in poof.toml, in the clear: 'poof config set token <token>' moves it into the encrypted file)"
	}
	return "(not set)"
}

func runConfigSet(cmd *cobra.Command, args []string) {
	key := args[0]
	value := ""
	if len(args) == 2 {
		value = args[1]
	}

	switch {
	case key == "server":
		if value == "" {
			value = config.ServerLocal
		}
		path := config.ClientConfigPath()
		if err := config.WriteClientSetting(path, activeProfile(), "server", value); err != nil {
			fatal("writing config: %v", err)
		}
		fmt.Printf("server = %q  →  %s%s\n", value, path, profileSuffix())

	case key == "token":
		if value == "" {
			fatal("value required: poof config set token <token>  (or '-' to read it from stdin)")
		}
		if value == "-" {
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			value = strings.TrimSpace(line)
			if value == "" {
				fatal("no token on standard input")
			}
		}
		profile := activeProfile()
		updateCredentials(func(s *secrets.Settings, v *secrets.Values) { secrets.SetAPIToken(s, v, profile, value) })
		fmt.Printf("token stored  →  %s%s\n", credentialsPath(), profileSuffix())
		// A clear-text copy in poof.toml would shadow nothing (the encrypted
		// one wins) but defeats the point; remove it.
		path := config.ClientConfigPath()
		if config.ClientFileHasToken(path, profile) {
			if err := config.DeleteClientSetting(path, profile, "token"); err != nil {
				fmt.Fprintf(os.Stderr, "  could not remove the clear-text token from %s: %v\n", path, err)
			} else {
				fmt.Printf("  clear-text token removed from %s\n", path)
			}
		}
		warnWeakProtection(loadCredentials())

	case isCredentialKey(key):
		setCredentialKey(key, value)

	case isServerConfigKey(key):
		if value == "" {
			fatal("value required: poof config set %s <value>", key)
		}
		requireServerConnection()
		if err := apiPatch("/config/"+key, map[string]string{"value": value}, nil); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("%s updated\n", key)

	default:
		fatal("unknown key %q\nknown keys: server, token, %s, %s", key, strings.Join(credentialKeys, ", "), strings.Join(serverConfigKeys, ", "))
	}
}

func runConfigUnset(cmd *cobra.Command, args []string) {
	key := args[0]
	if key == "token" {
		profile := activeProfile()
		updateCredentials(func(s *secrets.Settings, v *secrets.Values) { secrets.SetAPIToken(s, v, profile, "") })
		if err := config.DeleteClientSetting(config.ClientConfigPath(), profile, "token"); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("token cleared%s\n", profileSuffix())
		return
	}
	if isCredentialKey(key) {
		unsetCredentialKey(key)
		return
	}
	if !isServerConfigKey(key) {
		fatal("unknown key %q\nknown keys: %s, %s", key, strings.Join(credentialKeys, ", "), strings.Join(serverConfigKeys, ", "))
	}
	requireServerConnection()
	if err := apiDelete("/config/" + key); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("%s cleared\n", key)
}

func maskSecret(s string) string {
	if s == "" {
		return "(not set)"
	}
	if len(s) <= 4 {
		return strings.Repeat("•", len(s))
	}
	return s[:4] + strings.Repeat("•", 8)
}

func displayValue(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}
