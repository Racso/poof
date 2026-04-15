package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var refreshCmd = &cobra.Command{
	Use:   "refresh <name>",
	Short: "Re-sync GitHub secrets and workflow for a project",
	Long: `Re-sync the POOF_URL and POOF_TOKEN secrets and the deploy
workflow file in the project's GitHub repo. Skips the workflow
commit if the file is already up to date.

Useful after template changes or token migrations.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		var result map[string]string
		if err := apiPost("/projects/"+name+"/refresh", nil, &result); err != nil {
			fatal("%v", err)
		}

		if result["status"] == "ci removed" {
			fmt.Printf("✓ removed Poof-managed CI for %q\n", name)
		} else {
			fmt.Printf("✓ refreshed GitHub config for %q\n", name)
		}
	},
}

func init() {
	rootCmd.AddCommand(refreshCmd)
}
