package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var snapshotCmd = &cobra.Command{
	Use:   "snapshot <name>",
	Short: "Preserve a project's container for forensics (image + logs + inspect)",
	Long: `Commit the container's writable layer to a local image
(poof-snapshot/<name>:<timestamp>) and dump its logs and inspect output under
the server's data dir. Works while the project is paused (stopped container)
and does not disturb the container. Snapshot images are never touched by GC.

Typical incident flow: poof pause → poof snapshot → investigate / deploy fix
→ poof resume.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		var result map[string]string
		if err := apiPost("/projects/"+name+"/snapshot", nil, &result); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("✓ snapshotted %q\n", name)
		fmt.Printf("  image: %s\n", result["image"])
		fmt.Printf("  files: %s (on the server)\n", result["dir"])
	},
}

func init() {
	rootCmd.AddCommand(snapshotCmd)
}
