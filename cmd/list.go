package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all projects",
	Run: func(cmd *cobra.Command, args []string) {
		var projects []map[string]interface{}
		if err := apiGet("/projects", &projects); err != nil {
			fatal("%v", err)
		}

		if len(projects) == 0 {
			fmt.Println("no projects registered")
			return
		}

		fmt.Printf("%-20s %-35s %-8s %s\n", "NAME", "DOMAIN", "STATUS", "IMAGE")
		fmt.Printf("%-20s %-35s %-8s %s\n", "----", "------", "------", "-----")
		for _, p := range projects {
			name, _ := p["name"].(string)
			domain, _ := p["domain"].(string)
			image, _ := p["image"].(string)
			running, _ := p["running"].(bool)

			status := "stopped"
			if running {
				status = "running"
			}
			fmt.Printf("%-20s %-35s %-8s %s\n", name, domain, status, image)
		}
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
}
