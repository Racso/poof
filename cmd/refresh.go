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

		if err := apiPost("/projects/"+name+"/refresh", nil, nil); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ refreshed GitHub config for %q\n", name)
	},
}

func init() {
	rootCmd.AddCommand(refreshCmd)
}
