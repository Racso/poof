package cmd

import (
	"fmt"
	"strings"

	"github.com/racso/poof/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or set configuration",
	Args:  cobra.NoArgs,
	Run:   runConfigShow,
}

// serverConfigKeys are the settings stored on the server, in display order.
var serverConfigKeys = []string{"domain", "github-user", "github-app-id", "github-app-key-path", "github-token"}

var configSetCmd = &cobra.Command{
	Use:   "set <key> [value]",
	Short: "Set a configuration value",
	Long: `Set a configuration value.

Local keys (written to this machine's config file):
  server               URL of the Poof! server. Omit value on the server machine itself.
  token                API token for authenticating with the server.

Server keys (sent to the remote server — requires server and token to be set):
  domain               Default domain for new projects (e.g. mydomain.com).
  github-user          GitHub username used for automatic CI setup.
  github-app-id        GitHub App id used to manage CI and pull images.
  github-app-key-path  Path, on the server, to the App's private key (PEM).
  github-token         Legacy GitHub personal access token. Prefer the App.`,
	Args: cobra.RangeArgs(1, 2),
	Run:  runConfigSet,
}

var configUnsetCmd = &cobra.Command{
	Use:   "unset <key>",
	Short: "Clear a server configuration value",
	Long: `Clear a server configuration value.

Typically used to remove the legacy github-token once a GitHub App is configured.`,
	Args: cobra.ExactArgs(1),
	Run:  runConfigUnset,
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
	fmt.Printf("  %-20s  %s\n", "token", maskSecret(cfg.Token))

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
		v := displayValue(settings[key])
		if key == "github-token" {
			v = maskSecret(settings[key])
		}
		fmt.Printf("  %-20s  %s\n", key, v)
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
	if cfg.Token == "" {
		fatal("token not configured — run: poof config set token <token> first")
	}
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
			fatal("value required: poof config set token <token>")
		}
		path := config.ClientConfigPath()
		if err := config.WriteClientSetting(path, activeProfile(), "token", value); err != nil {
			fatal("writing config: %v", err)
		}
		fmt.Printf("token updated  →  %s%s\n", path, profileSuffix())

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
		fatal("unknown key %q\nknown keys: server, token, %s", key, strings.Join(serverConfigKeys, ", "))
	}
}

func runConfigUnset(cmd *cobra.Command, args []string) {
	key := args[0]
	if !isServerConfigKey(key) {
		fatal("unknown server key %q\nknown keys: %s", key, strings.Join(serverConfigKeys, ", "))
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
	// Servers from v0.28.0 on mask the PAT themselves; masking a masked value
	// again would just mangle it. Older servers still send it in the clear.
	if strings.Contains(s, "…") {
		return s
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
