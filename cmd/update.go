package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update <name>",
	Short: "Update a project's configuration",
	Long: `Update one or more configuration fields of an existing project.

Only the flags you pass will be changed; omitted flags are left as-is.
The project token is never affected — GitHub Actions integrations remain valid.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		domain, _ := cmd.Flags().GetString("domain")
		image, _ := cmd.Flags().GetString("image")
		repo, _ := cmd.Flags().GetString("repo")
		branch, _ := cmd.Flags().GetString("branch")
		port, _ := cmd.Flags().GetInt("port")

		payload := map[string]interface{}{}
		if domain != "" {
			payload["domain"] = domain
		}
		if image != "" {
			payload["image"] = image
		}
		if repo != "" {
			payload["repo"] = repo
		}
		if branch != "" {
			payload["branch"] = branch
		}
		if port != 0 {
			payload["port"] = port
		}

		if len(payload) == 0 {
			fatal("no fields to update — pass at least one flag")
		}

		var result map[string]interface{}
		if err := apiPatch("/projects/"+name, payload, &result); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ project %q updated\n", name)
		if d, ok := result["domain"].(string); ok {
			fmt.Printf("  domain:  %s\n", d)
		}
		if i, ok := result["image"].(string); ok {
			fmt.Printf("  image:   %s\n", i)
		}
		if r, ok := result["repo"].(string); ok {
			fmt.Printf("  repo:    %s\n", r)
		}
		if b, ok := result["branch"].(string); ok {
			fmt.Printf("  branch:  %s\n", b)
		}
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
	updateCmd.Flags().String("domain", "", "new domain")
	updateCmd.Flags().String("image", "", "new Docker image")
	updateCmd.Flags().String("repo", "", "new GitHub repo (owner/name)")
	updateCmd.Flags().String("branch", "", "new branch to deploy")
	updateCmd.Flags().Int("port", 0, "new container port")
}
