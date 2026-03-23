package cmd

import (
	"fmt"

	"github.com/racso/poof/defaults"
	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Register a new project",
	Long: fmt.Sprintf(`Register a new project with Poof!.

Defaults (all overridable with flags):
  --domain   <name>.<configured-domain>
  --image    ghcr.io/<github-user>/<name>
  --repo     <github-user>/<name>
  --branch   %s
  --port     %d

If a GitHub PAT is configured on the server, Poof! will automatically:
  - Set POOF_URL and POOF_TOKEN as repo secrets
  - Commit .github/workflows/poof.yml into the repo`, defaults.Branch, defaults.Port),
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		domain, _ := cmd.Flags().GetString("domain")
		image, _ := cmd.Flags().GetString("image")
		repo, _ := cmd.Flags().GetString("repo")
		branch, _ := cmd.Flags().GetString("branch")
		port, _ := cmd.Flags().GetInt("port")
		subpath, _ := cmd.Flags().GetString("subpath")

		payload := map[string]interface{}{
			"name":   name,
			"domain": domain,
			"image":  image,
			"repo":   repo,
			"branch": branch,
			"port":   port,
		}

		// Remove zero values so server applies its own defaults.
		if domain == "" {
			delete(payload, "domain")
		}
		if image == "" {
			delete(payload, "image")
		}
		if repo == "" {
			delete(payload, "repo")
		}
		if branch == "" {
			delete(payload, "branch")
		}
		if port == 0 {
			delete(payload, "port")
		}
		if subpath != "" {
			payload["subpath"] = subpath
		}

		var result map[string]interface{}
		if err := apiPost("/projects", payload, &result); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ project %q registered\n", name)
		if d, ok := result["domain"].(string); ok {
			fmt.Printf("  domain:  %s\n", d)
		}
		if i, ok := result["image"].(string); ok {
			fmt.Printf("  image:   %s\n", i)
		}
		if r, ok := result["repo"].(string); ok {
			fmt.Printf("  repo:    %s\n", r)
		}
	},
}

func init() {
	rootCmd.AddCommand(addCmd)
	addCmd.Flags().String("domain", "", "custom domain (default: <name>.<root-domain>)")
	addCmd.Flags().String("image", "", "Docker image (default: ghcr.io/<github-user>/<name>)")
	addCmd.Flags().String("repo", "", "GitHub repo owner/name (default: <github-user>/<name>)")
	addCmd.Flags().String("branch", "", fmt.Sprintf("branch to deploy (default: %s)", defaults.Branch))
	addCmd.Flags().Int("port", 0, fmt.Sprintf("container port (default: %d)", defaults.Port))
	addCmd.Flags().String("subpath", "", "subpath routing mode: disabled, redirect, or proxy (default: server's subpath_default)")
}
