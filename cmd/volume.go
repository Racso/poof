package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var volumeCmd = &cobra.Command{
	Use:   "volume",
	Short: "Manage persistent volumes for a project",
	Long: `Manage persistent volume mounts for a project.

Volumes are applied on the next deploy. After adding or removing a volume,
run 'poof deploy <project>' to recreate the container with the new mounts.`,
}

var volumeAddCmd = &cobra.Command{
	Use:   "add <project> <mount>",
	Short: "Add a volume mount to a project",
	Long: `Add a volume mount to a project.

MOUNT can be:

  /container/path
      Managed mount. Poof! will create and own the host directory at
      /var/lib/poof/<project>/<container-path>.

  /host/path:/container/path
      Explicit mount. You control the host directory.

Examples:
  poof volume add myapp /app/data
  poof volume add myapp /mnt/uploads:/app/uploads

Changes take effect on the next deploy:
  poof deploy myapp`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		project := args[0]
		mount := args[1]

		payload := map[string]interface{}{"mount": mount}

		var result map[string]interface{}
		if err := apiPost("/projects/"+project+"/volumes", payload, &result); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ volume added\n")
		if h, ok := result["host_path"].(string); ok {
			fmt.Printf("  host:      %s\n", h)
		}
		if c, ok := result["container_path"].(string); ok {
			fmt.Printf("  container: %s\n", c)
		}
		if m, ok := result["managed"].(bool); ok {
			if m {
				fmt.Printf("  managed:   yes\n")
			} else {
				fmt.Printf("  managed:   no\n")
			}
		}
		fmt.Printf("\nRedeploy to apply: poof deploy %s\n", project)
	},
}

var volumeListCmd = &cobra.Command{
	Use:   "list <project>",
	Short: "List volume mounts for a project",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		project := args[0]

		var vols []map[string]interface{}
		if err := apiGet("/projects/"+project+"/volumes", &vols); err != nil {
			fatal("%v", err)
		}

		if len(vols) == 0 {
			fmt.Printf("no volumes for project %q\n", project)
			return
		}

		fmt.Printf("%-4s  %-40s  %-25s  %s\n", "ID", "HOST PATH", "CONTAINER PATH", "MANAGED")
		for _, v := range vols {
			id := fmt.Sprintf("%.0f", v["id"])
			host, _ := v["host_path"].(string)
			container, _ := v["container_path"].(string)
			managed := "no"
			if m, ok := v["managed"].(bool); ok && m {
				managed = "yes"
			}
			fmt.Printf("%-4s  %-40s  %-25s  %s\n", id, host, container, managed)
		}
	},
}

var volumeRemoveCmd = &cobra.Command{
	Use:   "remove <project> <id>",
	Short: "Remove a volume mount from a project",
	Long: `Remove a volume mount from a project by its ID (from 'poof volume list').

By default only the mount registration is removed; host data is left intact.
Use --purge to also delete the host directory (managed volumes only).

Changes take effect on the next deploy.`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		project := args[0]
		id := args[1]
		purge, _ := cmd.Flags().GetBool("purge")

		path := "/projects/" + project + "/volumes/" + id
		if purge {
			path += "?purge=true"
		}

		// Fetch before deleting so we can show the host path in the output.
		var vol map[string]interface{}
		_ = apiGet("/projects/"+project+"/volumes/"+id, &vol)

		if err := apiDelete(path); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ volume %s removed\n", id)

		if hostPath, ok := vol["host_path"].(string); ok {
			managed, _ := vol["managed"].(bool)
			if managed {
				if purge {
					fmt.Printf("  host data at %s was deleted\n", hostPath)
				} else {
					fmt.Printf("\n⚠  Host data was NOT deleted. To remove it:\n")
					fmt.Printf("   poof volume remove %s %s --purge\n", project, id)
				}
			} else {
				fmt.Printf("\n  Host data at %s was NOT deleted (explicit mount — manage it yourself).\n", hostPath)
			}
		}

		fmt.Printf("\nRedeploy to apply: poof deploy %s\n", project)
	},
}

func init() {
	rootCmd.AddCommand(volumeCmd)
	volumeCmd.AddCommand(volumeAddCmd)
	volumeCmd.AddCommand(volumeListCmd)
	volumeCmd.AddCommand(volumeRemoveCmd)
	volumeRemoveCmd.Flags().Bool("purge", false, "also delete the host directory (managed volumes only)")
}
