package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/racso/poof/github"
	"github.com/spf13/cobra"
)

// ciAction mirrors server.CIAction: one file the repository should gain or
// lose so a project's CI matches its registration. The server computes
// them; this file performs them, because the credential able to write to a
// repository lives on the operator's machine and nowhere else.
type ciAction struct {
	Op      string `json:"op"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Message string `json:"message"`
}

// ciPlanned is the shape of every server answer that carries a plan.
type ciPlanned struct {
	CI []ciAction `json:"ci"`
}

var ciCmd = &cobra.Command{
	Use:   "ci",
	Short: "Work with a project's deploy workflow",
	Long: `Work with the GitHub Actions workflow that deploys a project.

Poof! never holds a GitHub credential on the server. When a command changes
what the workflow should look like (add, configure, clone, remove, refresh),
the server answers with the file to write or delete and this machine does it,
with the first of:

  1. the GitHub CLI's login, when 'gh' is installed and you allowed it
     ('poof config set gh yes'; asked once on first use);
  2. the token stored with 'poof config set github-token';
  3. the environment variable named by 'poof config set github-token-env';
  4. $GH_TOKEN or $GITHUB_TOKEN;
  5. nothing: the command prints the links to do it in a browser.

A token needs Contents and Workflows (read and write) on the repository, and
Actions (write) to dispatch runs. A gh login without the 'workflow' scope
gets it with 'gh auth refresh -s workflow'.

The stored token lives encrypted in credentials.json next to the client
config. Its key mixes $POOF_KEY from this machine's environment with a
passphrase you type ('poof config set passphrase'); either may be absent,
both absent is weak and said so. --passphrase-stdin feeds the passphrase
from standard input for scripts.`,
}

var ciPrintCmd = &cobra.Command{
	Use:   "print <name>",
	Short: "Print a project's deploy workflow (or the links to place it by hand)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		var plan ciPlanned
		if err := apiGet("/projects/"+args[0]+"/ci", &plan); err != nil {
			fatal("%v", err)
		}
		links, _ := cmd.Flags().GetBool("links")
		if links {
			for _, a := range plan.CI {
				printCILinks(a)
			}
			return
		}
		for _, a := range plan.CI {
			if a.Op == "write" {
				fmt.Print(a.Content)
				return
			}
		}
		fmt.Fprintf(os.Stderr, "CI is off for %s: the workflow %s should not exist in the repository.\n", args[0], ciPath(plan.CI))
	},
}

// ciActionsOf pulls the plan out of a loosely decoded server answer.
func ciActionsOf(result map[string]interface{}) []ciAction {
	raw, ok := result["ci"]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var actions []ciAction
	_ = json.Unmarshal(b, &actions)
	return actions
}

func ciPath(actions []ciAction) string {
	if len(actions) == 0 {
		return ""
	}
	return actions[0].Path
}

func init() {
	ciPrintCmd.Flags().Bool("links", false, "print the browser links for applying the plan by hand instead of the file")
	ciCmd.AddCommand(ciPrintCmd)
	rootCmd.AddCommand(ciCmd)
}

// ciLinksSuppressed is set by bulk commands (refresh --all): the browser
// links run to 2 KB each and are useless fifty times over.
var ciLinksSuppressed bool

// applyCI performs the plan with whatever credential this machine has, or
// prints how to finish by hand. Nothing here is fatal: the registration on
// the server already happened, and a workflow placed later is just as good.
func applyCI(actions []ciAction) {
	if len(actions) == 0 {
		return
	}
	token, source := githubCredential()
	if token == "" {
		fmt.Println("  GitHub not updated: no 'gh' login or $GH_TOKEN on this machine. To finish by hand:")
		byHand(actions)
		return
	}
	client := github.NewClient(token)
	for _, a := range actions {
		owner, repo, ok := strings.Cut(a.Repo, "/")
		if !ok {
			fmt.Printf("  ✗ %s: repository %q is not owner/name\n", a.Path, a.Repo)
			continue
		}
		var changed bool
		var err error
		switch a.Op {
		case "write":
			changed, err = client.PutFile(owner, repo, a.Branch, a.Path, a.Content, a.Message)
		case "delete":
			changed, err = client.DeleteFile(owner, repo, a.Branch, a.Path, a.Message)
		default:
			err = fmt.Errorf("unknown CI action %q", a.Op)
		}
		switch {
		case err != nil:
			fmt.Printf("  ✗ %s in %s@%s (via %s): %v\n", a.Path, a.Repo, a.Branch, source, err)
			if hint := ciErrorHint(err, source); hint != "" {
				fmt.Printf("    %s\n", hint)
			}
			if !isArchived(err) {
				fmt.Println("    By hand:")
				byHand([]ciAction{a})
			}
		case changed && a.Op == "write":
			fmt.Printf("  ✓ workflow committed to %s@%s (via %s)\n", a.Repo, a.Branch, source)
		case changed:
			fmt.Printf("  ✓ workflow removed from %s@%s (via %s)\n", a.Repo, a.Branch, source)
		case a.Op == "write":
			fmt.Printf("  ✓ workflow in %s@%s already up to date\n", a.Repo, a.Branch)
		default:
			fmt.Printf("  ✓ no workflow to remove in %s@%s\n", a.Repo, a.Branch)
		}
	}
}

func byHand(actions []ciAction) {
	if ciLinksSuppressed {
		fmt.Println("    (run 'poof refresh <name>' for one project to get the browser links)")
		return
	}
	for _, a := range actions {
		printCILinks(a)
	}
}

// isArchived recognises GitHub's refusal to write to an archived repository:
// no credential fixes that, so no hint about scopes applies.
func isArchived(err error) bool {
	return github.StatusOf(err) == http.StatusForbidden && strings.Contains(err.Error(), "archived")
}

// ciErrorHint turns the usual GitHub refusals into the fix.
func ciErrorHint(err error, source string) string {
	if isArchived(err) {
		return "the repository is archived (read-only): 'poof configure <name> --ci no' stops Poof! from trying, or 'poof remove <name>'"
	}
	switch github.StatusOf(err) {
	case http.StatusForbidden:
		if source == "gh" {
			return "gh's token lacks the 'workflow' scope: run 'gh auth refresh -s workflow' and retry with 'poof refresh <name>'"
		}
		return "the token needs Contents and Workflows (read and write) on this repository"
	case http.StatusUnauthorized:
		return "the token was rejected: expired or revoked"
	case http.StatusNotFound:
		return "repository not found, or the token cannot see it"
	}
	return ""
}

// printCILinks prints the browser route for one action: GitHub's new-file
// editor pre-filled with the workflow, or the delete page.
func printCILinks(a ciAction) {
	switch a.Op {
	case "write":
		q := url.Values{"filename": {a.Path}, "value": {a.Content}}
		fmt.Printf("    create %s on %s:\n      https://github.com/%s/new/%s?%s\n", a.Path, a.Branch, a.Repo, a.Branch, q.Encode())
		fmt.Printf("    (if the file already exists, edit it and paste the output of 'poof ci print <name>':\n      https://github.com/%s/edit/%s/%s)\n", a.Repo, a.Branch, a.Path)
	case "delete":
		fmt.Printf("    delete %s from %s:\n      https://github.com/%s/delete/%s/%s\n", a.Path, a.Branch, a.Repo, a.Branch, a.Path)
	}
}

// dispatchWorkflow fires a project's workflow by hand. With a tag, the job
// redeploys that image without rebuilding, which is how an image the server
// could not pull (it has no registry credential) gets onto the host: the
// job's own token pulls it.
func dispatchWorkflow(project, repo, branch, tag string) {
	file := fmt.Sprintf("poof-auto-ci-%s.yml", project)
	inputs := map[string]string{}
	if tag != "" {
		inputs["tag"] = tag
	}
	page := fmt.Sprintf("https://github.com/%s/actions/workflows/%s", repo, file)
	token, source := githubCredential()
	if token == "" {
		fmt.Printf("  no 'gh' login or $GH_TOKEN on this machine. Run the workflow in the browser")
		if tag != "" {
			fmt.Printf(" with tag=%s", tag)
		}
		fmt.Printf(":\n    %s\n", page)
		return
	}
	owner, name, _ := strings.Cut(repo, "/")
	if err := github.NewClient(token).Dispatch(owner, name, file, branch, inputs); err != nil {
		fmt.Printf("  ✗ dispatch via %s failed: %v\n", source, err)
		if github.StatusOf(err) == http.StatusForbidden {
			fmt.Println("    the token needs Actions (write) on the repository")
		}
		fmt.Printf("    Run it in the browser instead:\n    %s\n", page)
		os.Exit(1)
	}
	what := "a build of " + branch
	if tag != "" {
		what = "a redeploy of tag " + tag
	}
	fmt.Printf("  ✓ workflow dispatched (via %s): %s\n    %s\n", source, what, page)
}
