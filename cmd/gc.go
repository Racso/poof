package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	gcKeep    int
	gcDryRun  bool
	gcAll     bool
	gcSetKeep int
)

var gcCmd = &cobra.Command{
	Use:   "gc [project]",
	Short: "Delete old Docker images for a project",
	Long: `Delete cached Docker images for a project (or all projects with --all).

Retention is a single global rule: keep the N most recent images per image
repo. Images backing a running container are always kept, on any project.
--keep overrides the configured retention for this run only.`,
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

		var resp struct {
			Results []struct {
				Project string   `json:"project"`
				Removed []string `json:"removed"`
				Kept    []string `json:"kept"`
				Failed  []string `json:"failed"`
			} `json:"results"`
			DryRun     bool   `json:"dry_run"`
			Disabled   bool   `json:"disabled"`
			BytesFreed *int64 `json:"bytes_freed,omitempty"`
		}
		if err := apiPost("/gc", payload, &resp); err != nil {
			fatal("%v", err)
		}

		if resp.Disabled {
			fmt.Println("gc is disabled (poof gc on, or pass --keep to override for one run)")
			return
		}
		if len(resp.Results) == 0 {
			fmt.Println("no projects matched")
			return
		}

		verb := "removed"
		if resp.DryRun {
			verb = "would remove"
		}
		totalRemoved, totalKept, totalFailed := 0, 0, 0
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
			totalKept += len(r.Kept)
			totalFailed += len(r.Failed)
		}
		if len(resp.Results) > 1 {
			fmt.Printf("\ntotal: %s %d, kept %d", verb, totalRemoved, totalKept)
			if totalFailed > 0 {
				fmt.Printf(", %d failed", totalFailed)
			}
			fmt.Println()
		}
		if resp.BytesFreed != nil {
			fmt.Printf("freed: %s\n", humanBytes(*resp.BytesFreed))
		} else if resp.DryRun && totalRemoved > 0 {
			fmt.Println("(dry-run: actual bytes freed depends on layer sharing; run without --dry-run to see real reclaim)")
		}
	},
}

func humanBytes(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	const k = 1000.0
	units := []string{"kB", "MB", "GB", "TB", "PB"}
	v := float64(n) / k
	u := 0
	for v >= k && u < len(units)-1 {
		v /= k
		u++
	}
	return fmt.Sprintf("%.1f %s", v, units[u])
}

// putGCConfig sends a partial update; omitted fields keep their stored value.
func putGCConfig(payload map[string]interface{}) {
	if err := apiPut("/gc/config", payload, nil); err != nil {
		fatal("%v", err)
	}
}

var gcSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set how many images to keep (applies to every project)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if !cmd.Flags().Changed("keep") {
			fatal("--keep is required")
		}
		if gcSetKeep < 0 {
			fatal("--keep must be >= 0")
		}
		putGCConfig(map[string]interface{}{"keep": gcSetKeep, "disabled": false})
		fmt.Printf("✓ gc keeps the %d most recent images per project\n", gcSetKeep)
	},
}

var gcOffCmd = &cobra.Command{
	Use:   "off",
	Short: "Disable automatic GC",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		putGCConfig(map[string]interface{}{"disabled": true})
		fmt.Println("✓ gc disabled")
	},
}

var gcOnCmd = &cobra.Command{
	Use:   "on",
	Short: "Re-enable automatic GC with the stored retention",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		putGCConfig(map[string]interface{}{"disabled": false})
		fmt.Println("✓ gc enabled")
	},
}

var gcStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the GC retention policy",
	Run: func(cmd *cobra.Command, args []string) {
		var resp struct {
			Keep     int  `json:"keep"`
			Disabled bool `json:"disabled"`
		}
		if err := apiGet("/gc/status", &resp); err != nil {
			fatal("%v", err)
		}
		if resp.Disabled {
			fmt.Printf("gc: disabled (keep %d when re-enabled)\n", resp.Keep)
			return
		}
		fmt.Printf("gc: keep %d most recent images per project\n", resp.Keep)
	},
}

func init() {
	rootCmd.AddCommand(gcCmd)
	gcCmd.Flags().IntVar(&gcKeep, "keep", 0, "override retention for this run only")
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", false, "show what would be deleted without deleting")
	gcCmd.Flags().BoolVar(&gcAll, "all", false, "GC every project")

	gcCmd.AddCommand(gcSetCmd)
	gcSetCmd.Flags().IntVar(&gcSetKeep, "keep", 0, "keep the N most recent images")

	gcCmd.AddCommand(gcOffCmd)
	gcCmd.AddCommand(gcOnCmd)
	gcCmd.AddCommand(gcStatusCmd)
}
