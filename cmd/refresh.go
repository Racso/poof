package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var refreshAll bool

var refreshCmd = &cobra.Command{
	Use:   "refresh [name]",
	Short: "Regenerate the GitHub deploy workflow for a project",
	Long: `Regenerate the deploy workflow file in the project's GitHub repo.
Skips the commit if the file is already up to date.

Useful after template changes (for example to move a repo from the old
POOF_TOKEN secret to OIDC authentication). With --all, every
project is refreshed in turn; failures are reported per project and do
not stop the run.`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if refreshAll && len(args) > 0 {
			fatal("cannot combine project name with --all")
		}
		if !refreshAll && len(args) == 0 {
			fatal("project name required (or use --all)")
		}

		if !refreshAll {
			if err := refreshOne(args[0]); err != nil {
				fatal("%v", err)
			}
			return
		}

		var projects []struct {
			Name string `json:"name"`
		}
		if err := apiGet("/projects", &projects); err != nil {
			fatal("%v", err)
		}
		if len(projects) == 0 {
			fmt.Println("no projects")
			return
		}

		var failed int
		for _, p := range projects {
			if err := refreshOne(p.Name); err != nil {
				failed++
				fmt.Printf("  ✗ %s: %v\n", p.Name, err)
			}
		}
		fmt.Printf("\n%d of %d refreshed", len(projects)-failed, len(projects))
		if failed > 0 {
			fmt.Printf(", %d failed", failed)
		}
		fmt.Println()
		if failed > 0 {
			os.Exit(1)
		}
	},
}

// refreshOne refreshes a single project and prints the outcome. The error is
// returned rather than fatal'd so --all can carry on past a failure.
func refreshOne(name string) error {
	var result map[string]string
	if err := apiPost("/projects/"+name+"/refresh", nil, &result); err != nil {
		return err
	}
	if result["status"] == "ci removed" {
		fmt.Printf("  ✓ %s — Poof-managed CI removed\n", name)
	} else {
		fmt.Printf("  ✓ %s\n", name)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(refreshCmd)
	refreshCmd.Flags().BoolVar(&refreshAll, "all", false, "refresh every project")
}
