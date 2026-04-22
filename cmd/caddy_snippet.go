package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var caddyCmd = &cobra.Command{
	Use:   "caddy",
	Short: "Manage per-project Caddy configuration snippets",
}

var caddyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List projects that have custom Caddy snippets",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		var names []string
		if err := apiGet("/caddy/snippets", &names); err != nil {
			fatal("%v", err)
		}

		if len(names) == 0 {
			fmt.Println("no custom caddy snippets")
			return
		}
		for _, name := range names {
			fmt.Println(name)
		}
	},
}

var caddyGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Download a project's Caddy snippet for editing",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		var result map[string]interface{}
		if err := apiGet("/projects/"+name+"/caddy", &result); err != nil {
			fatal("%v", err)
		}

		content, _ := result["content"].(string)

		path := snippetPath(name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			fatal("cannot create temp dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			fatal("cannot write file: %v", err)
		}

		fmt.Printf("✓ snippet for %q saved to:\n  %s\n", name, path)
		fmt.Println("  edit the file, then run: poof caddy set", name)
	},
}

var caddySetForce bool

var caddySetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Push a local Caddy snippet to the server",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		path := snippetPath(name)
		data, err := os.ReadFile(path)
		if err != nil {
			fatal("cannot read snippet file: %v\n  run 'poof caddy get %s' first", err, name)
		}

		payload := map[string]interface{}{
			"content": string(data),
			"force":   caddySetForce,
		}

		if err := apiPut("/projects/"+name+"/caddy", payload, nil); err != nil {
			fatal("%v", err)
		}

		// Cleanup local file on success.
		os.Remove(path)

		fmt.Printf("✓ caddy snippet updated for %q\n", name)
	},
}

var caddyDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Remove a project's Caddy snippet",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		if err := apiDelete("/projects/" + name + "/caddy"); err != nil {
			fatal("%v", err)
		}

		// Also clean up any local file.
		os.Remove(snippetPath(name))

		fmt.Printf("✓ caddy snippet removed for %q\n", name)
	},
}

// snippetPath returns a deterministic local path for a project's snippet file.
func snippetPath(project string) string {
	return filepath.Join(os.TempDir(), "poof-caddy-"+project+".Caddyfile")
}

func init() {
	rootCmd.AddCommand(caddyCmd)
	caddyCmd.AddCommand(caddyListCmd)
	caddyCmd.AddCommand(caddyGetCmd)
	caddyCmd.AddCommand(caddySetCmd)
	caddyCmd.AddCommand(caddyDeleteCmd)

	caddySetCmd.Flags().BoolVar(&caddySetForce, "force", false, "skip concurrency check and push regardless")
}
