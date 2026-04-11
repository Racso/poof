package cmd

import (
	"bufio"
	"fmt"
	"os"
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
		strs := make([]string, len(keys))
		for i, k := range keys {
			strs[i] = fmt.Sprint(k)
		}
		fmt.Println(strings.Join(strs, ","))
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

var envCopyAll bool
var envCopyOnly string
var envCopyExcept string
var envCopyAsk bool

var envCopyCmd = &cobra.Command{
	Use:   "copy <source> <target>",
	Short: "Copy environment variables from one project to another",
	Long: `Copy environment variables from one project to another.
Exactly one selection flag is required:
  --all            copy all variables
  --only A,B,C     copy only the listed keys
  --except D,E,F   copy all except the listed keys
  --ask            interactively confirm each key`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		source := args[0]
		target := args[1]

		// Validate exactly one flag.
		flags := 0
		if envCopyAll {
			flags++
		}
		if envCopyOnly != "" {
			flags++
		}
		if envCopyExcept != "" {
			flags++
		}
		if envCopyAsk {
			flags++
		}
		if flags != 1 {
			fatal("exactly one of --all, --only, --except, or --ask is required")
		}

		// Fetch available keys from source.
		var result map[string]interface{}
		if err := apiGet("/projects/"+source+"/env", &result); err != nil {
			fatal("%v", err)
		}
		rawKeys, _ := result["keys"].([]interface{})
		if len(rawKeys) == 0 {
			fmt.Printf("no env vars in %q — nothing to copy\n", source)
			return
		}
		allKeys := make([]string, len(rawKeys))
		for i, k := range rawKeys {
			allKeys[i] = fmt.Sprint(k)
		}

		// Determine which keys to copy.
		var keys []string
		switch {
		case envCopyAll:
			keys = allKeys
		case envCopyOnly != "":
			keys = splitCSV(envCopyOnly)
			validateKeys(keys, allKeys, source)
		case envCopyExcept != "":
			excluded := toSet(splitCSV(envCopyExcept))
			for _, k := range allKeys {
				if !excluded[k] {
					keys = append(keys, k)
				}
			}
		case envCopyAsk:
			reader := bufio.NewReader(os.Stdin)
			for _, k := range allKeys {
				fmt.Printf("Copy %s? [y/N] ", k)
				line, _ := reader.ReadString('\n')
				line = strings.TrimSpace(strings.ToLower(line))
				if line == "y" || line == "yes" {
					keys = append(keys, k)
				}
			}
		}

		if len(keys) == 0 {
			fmt.Println("no keys selected — nothing to copy")
			return
		}

		// Ask server to copy.
		payload := map[string]interface{}{"keys": keys}
		var copyResult map[string]interface{}
		if err := apiPost("/projects/"+source+"/env/copy-to/"+target, payload, &copyResult); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ copied %d env var(s) from %q to %q\n", len(keys), source, target)
		fmt.Println("  run 'poof deploy' to apply changes")
	},
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

func validateKeys(requested, available []string, source string) {
	avail := toSet(available)
	for _, k := range requested {
		if !avail[k] {
			fatal("key %q does not exist in project %q", k, source)
		}
	}
}

func init() {
	rootCmd.AddCommand(envCmd)
	envCmd.AddCommand(envGetCmd)
	envCmd.AddCommand(envSetCmd)
	envCmd.AddCommand(envUnsetCmd)
	envCmd.AddCommand(envCopyCmd)

	envCopyCmd.Flags().BoolVar(&envCopyAll, "all", false, "copy all variables")
	envCopyCmd.Flags().StringVar(&envCopyOnly, "only", "", "copy only these keys (comma-separated)")
	envCopyCmd.Flags().StringVar(&envCopyExcept, "except", "", "copy all except these keys (comma-separated)")
	envCopyCmd.Flags().BoolVar(&envCopyAsk, "ask", false, "interactively confirm each key")
}
