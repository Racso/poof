package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "One-shot migrations for transitioning past breaking changes",
	Long: `Container for one-shot migrations introduced by breaking Poof! releases.

Each subcommand handles a specific transition; running it more than once is
safe (already-migrated projects are reported as such and skipped).`,
}

// --- workflows subcommand ---

type workflowDiagnostic struct {
	Project          string                  `json:"project"`
	Repo             string                  `json:"repo"`
	CI               bool                    `json:"ci"`
	OldPath          string                  `json:"old_path"`
	NewPath          string                  `json:"new_path"`
	OldPathExists    bool                    `json:"old_path_exists"`
	OldPathHasMarker bool                    `json:"old_path_has_marker"`
	NewPathExists    bool                    `json:"new_path_exists"`
	References       []workflowReference     `json:"references,omitempty"`
	Error            string                  `json:"error,omitempty"`
}

type workflowReference struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Hint string `json:"hint"`
}

type migrateWorkflowsResponse struct {
	Diagnostics []workflowDiagnostic `json:"diagnostics"`
}

// Possible states a project can be in. The server returns raw facts;
// we classify here so adding a new state never requires a server release.
const (
	stateMigrated     = "migrated"
	statePending      = "pending"
	statePendingNoMrk = "pending-no-marker"
	statePartial      = "partial"
	stateDisabled     = "disabled"
	stateNoWorkflow   = "no-workflow"
	stateError        = "error"
)

// classify returns a short state string for a single diagnostic. The CLI
// renders the same state consistently regardless of which subcommand
// surfaces it (today only `migrate workflows`, possibly more later).
func classify(d workflowDiagnostic) string {
	if d.Error != "" {
		return stateError
	}
	switch {
	case d.OldPathExists && d.NewPathExists:
		return statePartial
	case d.NewPathExists:
		return stateMigrated
	case d.OldPathExists && d.OldPathHasMarker:
		return statePending
	case d.OldPathExists && !d.OldPathHasMarker:
		return statePendingNoMrk
	case !d.CI:
		return stateDisabled
	default:
		return stateNoWorkflow
	}
}

var migrateWorkflowsCmd = &cobra.Command{
	Use:   "workflows [project]",
	Short: "Diagnose (or rename, with --apply) Poof-managed workflow files",
	Long: `Diagnose the GitHub state of every project's workflow file and report
which ones still live at the legacy path .github/workflows/poof-<name>.yml
versus the canonical v0.16.0+ path .github/workflows/poof-auto-ci-<name>.yml.

Diagnostic mode (default) is read-only and always safe.

Apply mode (--apply) actually performs the rename. To prevent fat-
fingered wide-blast operations, --apply REQUIRES an explicit scope:
  --all              every project Poof manages
  <project>          a single project (positional argument)
  --repo Owner/foo   every project in the given repo

Apply is idempotent: projects already at the new path are skipped.`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		apply, _ := cmd.Flags().GetBool("apply")
		all, _ := cmd.Flags().GetBool("all")
		repo, _ := cmd.Flags().GetString("repo")
		var project string
		if len(args) == 1 {
			project = args[0]
		}

		if apply {
			scopes := 0
			if all {
				scopes++
			}
			if repo != "" {
				scopes++
			}
			if project != "" {
				scopes++
			}
			if scopes != 1 {
				fatal("--apply requires exactly one of: --all, <project>, or --repo <owner/repo>")
			}
			runApply(project, repo)
			return
		}

		// Diagnostic mode (read-only). Scope flags are tolerated for symmetry
		// but unused here — the server always returns all projects and the
		// CLI filters in render. (Filter-on-server for diagnostic is a future
		// optimization if reports get large.)
		var resp migrateWorkflowsResponse
		if err := apiGet("/migrate/workflows", &resp); err != nil {
			fatal("%v", err)
		}

		// Tally + group references by repo for the bottom-of-output section.
		counts := map[string]int{}
		var refRepos []string
		refsByRepo := map[string][]workflowDiagnostic{}

		for _, d := range resp.Diagnostics {
			s := classify(d)
			counts[s]++

			fmt.Printf("\n%s (%s)\n", d.Project, d.Repo)
			fmt.Printf("  state:  %s\n", stateLabel(s))
			fmt.Printf("  paths:  %s%s\n", filePresence(d.OldPath, d.OldPathExists), markerNote(d))
			fmt.Printf("          %s\n", filePresence(d.NewPath, d.NewPathExists))
			if action := actionFor(s, d); action != "" {
				fmt.Printf("  action: %s\n", action)
			}
			if d.Error != "" {
				fmt.Printf("  error:  %s\n", d.Error)
			}
			if len(d.References) > 0 {
				if _, seen := refsByRepo[d.Repo]; !seen {
					refRepos = append(refRepos, d.Repo)
				}
				refsByRepo[d.Repo] = append(refsByRepo[d.Repo], d)
			}
		}

		// References block — surface user-owned files that would break
		// after the rename (their `uses:` points at the old path).
		if len(refRepos) > 0 {
			fmt.Printf("\nReferences to legacy workflow paths found in user-owned files:\n")
			for _, repo := range refRepos {
				for _, d := range refsByRepo[repo] {
					for _, r := range d.References {
						fmt.Printf("  %s: %s:%d  %s\n", d.Repo, r.Path, r.Line, r.Hint)
					}
				}
			}
			fmt.Println("  (these are not auto-rewritten; update them manually before --apply)")
		}

		// Summary
		fmt.Printf("\nSummary\n")
		fmt.Printf("  %3d project(s) scanned\n", len(resp.Diagnostics))
		printCount(counts, stateMigrated, "already migrated")
		printCount(counts, statePending, "pending — safe to rename")
		printCount(counts, statePendingNoMrk, "pending — no marker (manual review)")
		printCount(counts, statePartial, "partial — both paths exist")
		printCount(counts, stateDisabled, "disabled (CI off)")
		printCount(counts, stateNoWorkflow, "no workflow at either path")
		printCount(counts, stateError, "errored during scan")

		if counts[statePending]+counts[statePartial] > 0 {
			fmt.Println("\n--apply is not yet implemented; for now, run 'poof refresh <name>' on each pending project to write the new path, then delete the old file manually.")
		}
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
	migrateCmd.AddCommand(migrateWorkflowsCmd)
	migrateWorkflowsCmd.Flags().Bool("apply", false, "actually rename files (requires --all, <project>, or --repo)")
	migrateWorkflowsCmd.Flags().Bool("all", false, "with --apply: target every project Poof manages")
	migrateWorkflowsCmd.Flags().String("repo", "", "with --apply: target every project in this repo (owner/name)")
}

// --- apply ---

type applyResult struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Status  string `json:"status"`            // "renamed" | "skipped" | "partial" | "error"
	Reason  string `json:"reason,omitempty"`  // populated when Status == "skipped"
	Error   string `json:"error,omitempty"`   // populated when Status == "error" or "partial"
}

type applyResponse struct {
	Results []applyResult `json:"results"`
}

func runApply(project, repo string) {
	body := map[string]string{}
	if project != "" {
		body["project"] = project
	}
	if repo != "" {
		body["repo"] = repo
	}

	var resp applyResponse
	if err := apiPost("/migrate/workflows", body, &resp); err != nil {
		fatal("%v", err)
	}

	counts := map[string]int{}
	for _, r := range resp.Results {
		counts[r.Status]++

		switch r.Status {
		case "renamed":
			fmt.Printf("✓ %s (%s)\n  %s → %s\n", r.Project, r.Repo, r.OldPath, r.NewPath)
		case "skipped":
			fmt.Printf("- %s (%s)  skipped (%s)\n", r.Project, r.Repo, r.Reason)
		case "partial":
			fmt.Printf("⚠ %s (%s)  partial — %s\n  new path written; legacy file still present\n", r.Project, r.Repo, r.Error)
		case "error":
			fmt.Printf("✗ %s (%s)  %s\n", r.Project, r.Repo, r.Error)
		default:
			fmt.Printf("? %s (%s)  unknown status %q\n", r.Project, r.Repo, r.Status)
		}
	}

	fmt.Printf("\nDone. ")
	parts := []string{}
	if c := counts["renamed"]; c > 0 {
		parts = append(parts, fmt.Sprintf("%d renamed", c))
	}
	if c := counts["skipped"]; c > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", c))
	}
	if c := counts["partial"]; c > 0 {
		parts = append(parts, fmt.Sprintf("%d partial", c))
	}
	if c := counts["error"]; c > 0 {
		parts = append(parts, fmt.Sprintf("%d error", c))
	}
	if len(parts) == 0 {
		fmt.Println("nothing to do.")
	} else {
		fmt.Println(strings.Join(parts, ", ") + ".")
	}
}

// --- rendering helpers ---

func stateLabel(s string) string {
	switch s {
	case stateMigrated:
		return "migrated ✓"
	case statePending:
		return "pending — safe to rename"
	case statePendingNoMrk:
		return "pending — no marker (manual review)"
	case statePartial:
		return "partial — both paths exist"
	case stateDisabled:
		return "disabled (CI off)"
	case stateNoWorkflow:
		return "no workflow at either path"
	case stateError:
		return "error"
	default:
		return s
	}
}

func filePresence(path string, exists bool) string {
	if exists {
		return path + " (present)"
	}
	return path + " (absent)"
}

func markerNote(d workflowDiagnostic) string {
	if !d.OldPathExists {
		return ""
	}
	if d.OldPathHasMarker {
		return " — marker present"
	}
	return " — no marker"
}

func actionFor(state string, d workflowDiagnostic) string {
	switch state {
	case statePending:
		return fmt.Sprintf("would rename to %s", d.NewPath)
	case statePendingNoMrk:
		return "skip unless --include-unmanaged is given (file lacks the marker; could be user-authored)"
	case statePartial:
		return "would delete the old path; the new path is authoritative"
	default:
		return ""
	}
}

func printCount(counts map[string]int, state, label string) {
	if c := counts[state]; c > 0 {
		fmt.Printf("  %3d %s\n", c, label)
	}
}
