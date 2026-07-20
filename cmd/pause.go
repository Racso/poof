package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pauseCmd = &cobra.Command{
	Use:   "pause <name>",
	Short: "Take a project offline (503 on all routes) without touching its config",
	Long: `Take a project offline immediately. All routes on the project's domain
respond 503 and the container is stopped (not removed — "poof resume" restores
the identical container) until "poof resume". The registration (repo, port,
domain, env vars, custom Caddy snippet) is untouched.

Deploys while paused are staged: the new container is created but not started,
so a fix can be applied before resuming. Use "poof snapshot" first to preserve
the current container for forensics.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runPauseResume(args[0], "pause")
	},
}

var resumeCmd = &cobra.Command{
	Use:   "resume <name>",
	Short: "Put a paused project back online with its previous routing",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runPauseResume(args[0], "resume")
	},
}

func runPauseResume(name, action string) {
	var result map[string]string
	if err := apiPost("/projects/"+name+"/"+action, nil, &result); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("✓ %s %q\n", result["status"], name)
	if d := result["domain"]; d != "" {
		if action == "pause" {
			fmt.Printf("  https://%s now responds 503 — run `poof resume %s` to restore\n", d, name)
		} else {
			fmt.Printf("  https://%s\n", d)
		}
	}
}

func init() {
	rootCmd.AddCommand(pauseCmd)
	rootCmd.AddCommand(resumeCmd)
}
