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

		payload := map[string]interface{}{
			"suffix": suffix,
		}

		// Resolve env keys if --env is set.
		if cloneEnv {
			payload["env_keys"] = resolveEnvKeys(source, cloneEnvAll, cloneEnvOnly, cloneEnvExcept, cloneEnvAsk)
		}

		var result map[string]interface{}
		if err := apiPost("/projects/"+source+"/clone", payload, &result); err != nil {
			fatal("%v", err)
		}

		proj, _ := result["project"].(map[string]interface{})
		cloneName := source + "-" + suffix

		fmt.Printf("✓ cloned %q → %q (branch: %s)\n", source, cloneName, suffix)
		if proj != nil {
			if d, ok := proj["domain"].(string); ok {
				fmt.Printf("  domain:  %s\n", d)
			}
			if i, ok := proj["image"].(string); ok {
				fmt.Printf("  image:   %s\n", i)
			}
			if r, ok := proj["repo"].(string); ok {
				fmt.Printf("  repo:    %s\n", r)
			}
			if f, ok := proj["folder"].(string); ok && f != "" {
				fmt.Printf("  folder:  %s\n", f)
			}
		}

		if copied, ok := result["env_keys_copied"].([]interface{}); ok && len(copied) > 0 {
			fmt.Printf("  env:     %d var(s) copied\n", len(copied))
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
