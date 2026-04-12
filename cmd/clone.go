package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var cloneEnv bool
var cloneEnvAll bool
var cloneEnvOnly string
var cloneEnvExcept string
var cloneEnvAsk bool

var cloneCmd = &cobra.Command{
	Use:   "clone <project> <suffix>",
	Short: "Clone a project's configuration under a new name",
	Long: `Clone a project's configuration under a new name.

The new project is named <project>-<suffix> and deploys from the
<suffix> branch. Domain, image, repo, port, subpath, and folder
are copied from the source project (domain adjusted for the new name).

Examples:
  poof clone myapp test              # creates myapp-test, deploys from "test" branch
  poof clone myapp staging --env --all  # same, plus copies all env vars

Use --env to also copy environment variables (requires exactly one of
--all, --only, --except, or --ask).`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		source := args[0]
		suffix := args[1]
		cloneName := source + "-" + suffix

		// Fetch source project.
		var result map[string]interface{}
		if err := apiGet("/projects/"+source, &result); err != nil {
			fatal("%v", err)
		}
		proj, ok := result["project"].(map[string]interface{})
		if !ok {
			fatal("could not read project %q", source)
		}

		// Build the new project payload from source, overriding name and branch.
		payload := map[string]interface{}{
			"name":   cloneName,
			"branch": suffix,
		}

		// Copy config from source, adjusting domain.
		if repo, ok := proj["repo"].(string); ok && repo != "" {
			payload["repo"] = repo
		}
		if image, ok := proj["image"].(string); ok && image != "" {
			payload["image"] = image
		}
		if port, ok := proj["port"].(float64); ok && port != 0 {
			payload["port"] = int(port)
		}
		if subpath, ok := proj["subpath"].(string); ok && subpath != "" {
			payload["subpath"] = subpath
		}
		if folder, ok := proj["folder"].(string); ok && folder != "" {
			payload["folder"] = folder
		}
		// Domain: leave empty so the server generates <cloneName>.<root-domain>.

		var createResult map[string]interface{}
		if err := apiPost("/projects", payload, &createResult); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ cloned %q → %q (branch: %s)\n", source, cloneName, suffix)
		if d, ok := createResult["domain"].(string); ok {
			fmt.Printf("  domain:  %s\n", d)
		}
		if i, ok := createResult["image"].(string); ok {
			fmt.Printf("  image:   %s\n", i)
		}
		if r, ok := createResult["repo"].(string); ok {
			fmt.Printf("  repo:    %s\n", r)
		}
		if f, ok := createResult["folder"].(string); ok && f != "" {
			fmt.Printf("  folder:  %s\n", f)
		}

		// Copy env vars if requested.
		if cloneEnv {
			copyEnvVars(source, cloneName, cloneEnvAll, cloneEnvOnly, cloneEnvExcept, cloneEnvAsk)
		}
	},
}

func init() {
	rootCmd.AddCommand(cloneCmd)
	cloneCmd.Flags().BoolVar(&cloneEnv, "env", false, "also copy environment variables (requires a selection flag)")
	cloneCmd.Flags().BoolVar(&cloneEnvAll, "all", false, "copy all env variables")
	cloneCmd.Flags().StringVar(&cloneEnvOnly, "only", "", "copy only these env keys (comma-separated)")
	cloneCmd.Flags().StringVar(&cloneEnvExcept, "except", "", "copy all env keys except these (comma-separated)")
	cloneCmd.Flags().BoolVar(&cloneEnvAsk, "ask", false, "interactively confirm each env key")
}
