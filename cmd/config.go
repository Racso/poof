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

var configSetCmd = &cobra.Command{
	Use:   "set <key> [value]",
	Short: "Set a configuration value",
	Long: `Set a configuration value.

Local key (written to this machine's config file only):
  server        URL of the Poof! server. Omit value on the server machine itself.

Server keys (sent to the server and also saved locally where applicable):
  token         API token for authenticating with the server.
  domain        Default domain for new projects (e.g. mydomain.com).
  github-user   GitHub username used for automatic CI setup.
  github-token  GitHub personal access token.`,
	Args: cobra.RangeArgs(1, 2),
	Run:  runConfigSet,
}

func init() {
	configCmd.AddCommand(configSetCmd)
	rootCmd.AddCommand(configCmd)
}

func runConfigShow(cmd *cobra.Command, args []string) {
	path := config.ClientConfigPath()
	fmt.Printf("client config  %s\n", path)
	fmt.Printf("  %-14s  %s\n", "server", displayValue(cfg.Server))
	fmt.Printf("  %-14s  %s\n", "token", maskSecret(cfg.Token))

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
	for _, key := range []string{"domain", "github-user", "github-token"} {
		v := displayValue(settings[key])
		if key == "github-token" {
			v = maskSecret(settings[key])
		}
		fmt.Printf("  %-14s  %s\n", key, v)
	}
}

func runConfigSet(cmd *cobra.Command, args []string) {
	key := args[0]
	value := ""
	if len(args) == 2 {
		value = args[1]
	}

	switch key {
	case "server":
		if value == "" {
			value = config.ServerLocal
		}
		path := config.ClientConfigPath()
		if err := config.WriteClientSetting(path, "server", value); err != nil {
			fatal("writing config: %v", err)
		}
		fmt.Printf("server = %q  →  %s\n", value, path)

	case "token":
		if value == "" {
			fatal("value required: poof config set token <token>")
		}
		if cfg.Server == "" {
			fatal("server not configured — run: poof config set server <url> first")
		}
		if err := apiPatch("/config/token", map[string]string{"value": value}, nil); err != nil {
			fatal("%v", err)
		}
		path := config.ClientConfigPath()
		if err := config.WriteClientSetting(path, "token", value); err != nil {
			fatal("writing config: %v", err)
		}
		fmt.Printf("token updated\n")

	case "domain", "github-user", "github-token":
		if value == "" {
			fatal("value required: poof config set %s <value>", key)
		}
		if cfg.Server == "" {
			fatal("server not configured — run: poof config set server <url> first")
		}
		if cfg.Token == "" {
			fatal("token not configured — run: poof config set token <token> first")
		}
		if err := apiPatch("/config/"+key, map[string]string{"value": value}, nil); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("%s updated\n", key)

	default:
		fatal("unknown key %q\nknown keys: server, token, domain, github-user, github-token", key)
	}
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
