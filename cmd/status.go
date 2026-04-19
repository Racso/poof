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
		if img, _ := p["image"].(string); img != "" {
			fmt.Printf("image:   %s\n", img)
		}
		fmt.Printf("repo:    %s\n", p["repo"])
		if folder, _ := p["folder"].(string); folder != "" {
			fmt.Printf("folder:  %s\n", folder)
		}
		fmt.Printf("branch:  %s\n", p["branch"])
		if port, _ := p["port"].(float64); port != 0 {
			fmt.Printf("port:    %.0f\n", port)
		}
		if static, _ := p["static"].(string); static != "" {
			fmt.Printf("static:  %s\n", static)
		}
		if build, _ := p["build"].(bool); build {
			fmt.Printf("build:   yes\n")
		}

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
