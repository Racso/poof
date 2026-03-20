package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show project details and last deployment",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		var result map[string]interface{}
		if err := apiGet("/projects/"+name, &result); err != nil {
			fatal("%v", err)
		}

		p, _ := result["project"].(map[string]interface{})
		running, _ := result["running"].(bool)
		dep, _ := result["deployment"].(map[string]interface{})

		status := "stopped"
		if running {
			status = "running"
		}

		fmt.Printf("name:    %s\n", p["name"])
		fmt.Printf("status:  %s\n", status)
		fmt.Printf("domain:  %s\n", p["domain"])
		fmt.Printf("image:   %s\n", p["image"])
		fmt.Printf("repo:    %s\n", p["repo"])
		fmt.Printf("branch:  %s\n", p["branch"])
		fmt.Printf("port:    %.0f\n", p["port"])

		if dep != nil {
			fmt.Printf("\nlast deployment:\n")
			fmt.Printf("  image:  %s\n", dep["image"])
			fmt.Printf("  status: %s\n", dep["status"])
			fmt.Printf("  at:     %s\n", dep["deployed_at"])
		} else {
			fmt.Printf("\nno deployments yet\n")
		}
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
