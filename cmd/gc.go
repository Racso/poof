package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var (
	gcKeep       int
	gcOlderThan  int
	gcDryRun     bool
	gcAll        bool
	gcSetKeep    int
	gcSetOlder   int
	gcSetAll     bool
	gcOffAll     bool
)

var gcCmd = &cobra.Command{
	Use:   "gc [project]",
	Short: "Delete old Docker images for a project",
	Long: `Delete cached Docker images for a project (or all projects with --all).

Without flags, the project's policy is used (or the built-in default of keep=3).
Flags override the policy for this run only.

When both --keep and --older-than are given, an image must satisfy BOTH
conditions to be deleted (outside the keep window AND older than N days).
For OR semantics, run two separate calls.`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if !gcAll && len(args) == 0 {
			fatal("project name required (or use --all)")
		}
		if gcAll && len(args) > 0 {
			fatal("cannot combine project name with --all")
		}

		payload := map[string]interface{}{"dry_run": gcDryRun}
		if gcAll {
			payload["all"] = true
		} else {
			payload["project"] = args[0]
		}
		if cmd.Flags().Changed("keep") {
			payload["keep"] = gcKeep
		}
		if cmd.Flags().Changed("older-than") {
			payload["older_than_days"] = gcOlderThan
		}

		var resp struct {
			Results []struct {
				Project string   `json:"project"`
				Removed []string `json:"removed"`
				Kept    []string `json:"kept"`
				Failed  []string `json:"failed"`
			} `json:"results"`
			DryRun bool `json:"dry_run"`
		}
		if err := apiPost("/gc", payload, &resp); err != nil {
			fatal("%v", err)
		}

		if len(resp.Results) == 0 {
			fmt.Println("no projects matched (static projects and projects with GC disabled are skipped)")
			return
		}

		verb := "removed"
		if resp.DryRun {
			verb = "would remove"
		}
		totalRemoved, totalFailed := 0, 0
		for _, r := range resp.Results {
			fmt.Printf("%s: %s %d, kept %d", r.Project, verb, len(r.Removed), len(r.Kept))
			if len(r.Failed) > 0 {
				fmt.Printf(", failed %d", len(r.Failed))
			}
			fmt.Println()
			for _, ref := range r.Removed {
				fmt.Printf("  - %s\n", ref)
			}
			for _, msg := range r.Failed {
				fmt.Printf("  ! %s\n", msg)
			}
			totalRemoved += len(r.Removed)
			totalFailed += len(r.Failed)
		}
		if len(resp.Results) > 1 {
			fmt.Printf("\ntotal: %s %d", verb, totalRemoved)
			if totalFailed > 0 {
				fmt.Printf(", %d failed", totalFailed)
			}
			fmt.Println()
		}
	},
}

var gcSetCmd = &cobra.Command{
	Use:   "set [project]",
	Short: "Set the GC retention policy for a project (or --all for global)",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if !gcSetAll && len(args) == 0 {
			fatal("project name required (or use --all for the global default)")
		}
		if gcSetAll && len(args) > 0 {
			fatal("cannot combine project name with --all")
		}
		if !cmd.Flags().Changed("keep") && !cmd.Flags().Changed("older-than") {
			fatal("at least one of --keep or --older-than is required")
		}

		target := "_default"
		if !gcSetAll {
			target = args[0]
		}

		payload := map[string]interface{}{}
		if cmd.Flags().Changed("keep") {
			payload["keep_count"] = gcSetKeep
		}
		if cmd.Flags().Changed("older-than") {
			payload["older_than_days"] = gcSetOlder
		}

		if err := apiPut("/gc/policy/"+target, payload, nil); err != nil {
			fatal("%v", err)
		}

		label := target
		if target == "_default" {
			label = "global default"
		}
		fmt.Printf("✓ gc policy updated for %s\n", label)
	},
}

var gcOffCmd = &cobra.Command{
	Use:   "off [project]",
	Short: "Disable automatic GC for a project (or --all for global)",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if !gcOffAll && len(args) == 0 {
			fatal("project name required (or use --all)")
		}
		if gcOffAll && len(args) > 0 {
			fatal("cannot combine project name with --all")
		}

		target := "_default"
		if !gcOffAll {
			target = args[0]
		}

		payload := map[string]interface{}{"disabled": true}
		if err := apiPut("/gc/policy/"+target, payload, nil); err != nil {
			fatal("%v", err)
		}

		label := target
		if target == "_default" {
			label = "global default"
		}
		fmt.Printf("✓ gc disabled for %s\n", label)
	},
}

var gcStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the GC policy for each project",
	Run: func(cmd *cobra.Command, args []string) {
		var resp struct {
			Policies []struct {
				Project       string `json:"project"`
				KeepCount     *int   `json:"keep_count"`
				OlderThanDays *int   `json:"older_than_days"`
				Disabled      bool   `json:"disabled"`
			} `json:"policies"`
			Resolved []struct {
				Project       string `json:"project"`
				KeepCount     *int   `json:"keep_count"`
				OlderThanDays *int   `json:"older_than_days"`
				Enabled       bool   `json:"enabled"`
				Source        string `json:"source"`
			} `json:"resolved"`
		}
		if err := apiGet("/gc/status", &resp); err != nil {
			fatal("%v", err)
		}

		// Show the global default first if it's set.
		var globalLine string
		for _, p := range resp.Policies {
			if p.Project == "*" {
				globalLine = formatPolicy(p.KeepCount, p.OlderThanDays, p.Disabled)
				break
			}
		}
		if globalLine == "" {
			globalLine = "keep 3 (built-in)"
		}
		fmt.Printf("global default: %s\n\n", globalLine)

		if len(resp.Resolved) == 0 {
			fmt.Println("no container projects")
			return
		}

		fmt.Printf("%-20s %-12s %s\n", "PROJECT", "SOURCE", "POLICY")
		fmt.Printf("%-20s %-12s %s\n", "-------", "------", "------")
		for _, r := range resp.Resolved {
			policy := formatPolicy(r.KeepCount, r.OlderThanDays, !r.Enabled)
			fmt.Printf("%-20s %-12s %s\n", r.Project, r.Source, policy)
		}
	},
}

func formatPolicy(keep, older *int, disabled bool) string {
	if disabled {
		return "disabled"
	}
	var parts []string
	if keep != nil {
		parts = append(parts, fmt.Sprintf("keep %d", *keep))
	}
	if older != nil {
		parts = append(parts, fmt.Sprintf("older-than %dd", *older))
	}
	if len(parts) == 0 {
		return "(no rules)"
	}
	return strings.Join(parts, " AND ")
}

func init() {
	rootCmd.AddCommand(gcCmd)
	gcCmd.Flags().IntVar(&gcKeep, "keep", 0, "keep the N most recent images, delete the rest")
	gcCmd.Flags().IntVar(&gcOlderThan, "older-than", 0, "delete images older than N days")
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", false, "show what would be deleted without deleting")
	gcCmd.Flags().BoolVar(&gcAll, "all", false, "GC every project")

	gcCmd.AddCommand(gcSetCmd)
	gcSetCmd.Flags().IntVar(&gcSetKeep, "keep", 0, "keep the N most recent images")
	gcSetCmd.Flags().IntVar(&gcSetOlder, "older-than", 0, "delete images older than N days")
	gcSetCmd.Flags().BoolVar(&gcSetAll, "all", false, "set the global default policy")

	gcCmd.AddCommand(gcOffCmd)
	gcOffCmd.Flags().BoolVar(&gcOffAll, "all", false, "disable GC globally")

	gcCmd.AddCommand(gcStatusCmd)
}
