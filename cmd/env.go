package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage environment variables for a project",
}

var envGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "List environment variable keys (values are never shown)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		var result map[string]interface{}
		if err := apiGet("/projects/"+name+"/env", &result); err != nil {
			fatal("%v", err)
		}

		keys, _ := result["keys"].([]interface{})
		if len(keys) == 0 {
			fmt.Println("no environment variables set")
			return
		}
		for _, k := range keys {
			fmt.Println(k)
		}
	},
}

var envSetCmd = &cobra.Command{
	Use:   "set <name> KEY=VALUE [KEY=VALUE ...]",
	Short: "Set one or more environment variables",
	Args:  cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		vars := map[string]string{}

		for _, pair := range args[1:] {
			k, v, found := strings.Cut(pair, "=")
			if !found {
				fatal("invalid format %q — expected KEY=VALUE", pair)
			}
			vars[k] = v
		}

		if err := apiPut("/projects/"+name+"/env", vars, nil); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ updated %d env var(s) for %q\n", len(vars), name)
		fmt.Println("  run 'poof deploy' to apply changes")
	},
}

var envUnsetCmd = &cobra.Command{
	Use:   "unset <name> <KEY>",
	Short: "Remove an environment variable",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		key := args[1]

		if err := apiDelete("/projects/" + name + "/env/" + key); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ removed %q from %q\n", key, name)
	},
}

func init() {
	rootCmd.AddCommand(envCmd)
	envCmd.AddCommand(envGetCmd)
	envCmd.AddCommand(envSetCmd)
	envCmd.AddCommand(envUnsetCmd)
}
