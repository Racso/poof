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
var serverConfigKeys = []string{"domain", "github-user"}

var configSetCmd = &cobra.Command{
	Use:   "set <key> [value]",
	Short: "Set a configuration value",
	Long: `Set a configuration value.

Local keys (written to this machine's config file):
  server               URL of the Poof! server. Omit value on the server machine itself.
  token                API token for authenticating with the server.

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
