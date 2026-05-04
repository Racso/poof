package cmd

import (
	"fmt"

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
	Use:   "workflows",
	Short: "Diagnose (or rename, with --apply) Poof-managed workflow files",
	Long: `Diagnose the GitHub state of every project's workflow file and report
which ones still live at the legacy path .github/workflows/poof-<name>.yml
versus the canonical v0.16.0+ path .github/workflows/poof-auto-ci-<name>.yml.

By default this is read-only — the report tells you what would happen,
nothing is changed on GitHub. (--apply is not yet implemented; coming in
a follow-up release.)`,
	Run: func(cmd *cobra.Command, args []string) {
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
