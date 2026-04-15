package cmd

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type projectSpec struct {
	Name   string
	Domain string
	Image  string
	Repo   string
	Branch string
	Port   int
	Static string
	CI     string // "yes"/"no" or empty (server default)
}

type remoteProject struct {
	Name    string `json:"name"`
	Domain  string `json:"domain"`
	Image   string `json:"image"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Port    int    `json:"port"`
	Static  string `json:"static"`
	CI      bool   `json:"ci"`
	Running bool   `json:"running"`
}

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Reconcile server state with a declarative projects file",
	Long: `Apply reads a projects file and reconciles it against the server:

  - Projects in the file but not on the server are added.
  - Projects on the server with changed fields are updated (and redeployed if running).
  - Projects on the server not in the file are left alone unless --prune is set.

Secrets (env vars, tokens) are not managed by the projects file.

Example file (poof.ini):

  [myapp]

  [api]
  domain = api.example.com
  port   = 3000

  [worker]
  image  = ghcr.io/myorg/worker
  branch = stable`,
	Run: func(cmd *cobra.Command, args []string) {
		filePath, _ := cmd.Flags().GetString("file")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		prune, _ := cmd.Flags().GetBool("prune")

		desired, err := parseProjectsFile(filePath)
		if err != nil {
			fatal("reading %s: %v", filePath, err)
		}

		var current []remoteProject
		if err := apiGet("/projects", &current); err != nil {
			fatal("%v", err)
		}

		currentMap := map[string]remoteProject{}
		for _, p := range current {
			currentMap[p.Name] = p
		}

		var toAdd []projectSpec
		type updateEntry struct {
			spec    projectSpec
			patch   map[string]interface{}
			running bool
		}
		var toUpdate []updateEntry
		var toRemove []remoteProject

		// Stable iteration order
		names := make([]string, 0, len(desired))
		for name := range desired {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			spec := desired[name]
			cur, exists := currentMap[name]
			if !exists {
				toAdd = append(toAdd, spec)
				continue
			}
			patch := buildPatch(spec, cur)
			if len(patch) > 0 {
				toUpdate = append(toUpdate, updateEntry{spec, patch, cur.Running})
			}
		}

		if prune {
			pruneNames := make([]string, 0)
			for name := range currentMap {
				if _, inDesired := desired[name]; !inDesired {
					pruneNames = append(pruneNames, name)
				}
			}
			sort.Strings(pruneNames)
			for _, name := range pruneNames {
				toRemove = append(toRemove, currentMap[name])
			}
		}

		if len(toAdd) == 0 && len(toUpdate) == 0 && len(toRemove) == 0 {
			fmt.Println("Nothing to do — server matches desired state.")
			return
		}

		for _, spec := range toAdd {
			fmt.Printf("  + %s\n", spec.Name)
		}
		for _, u := range toUpdate {
			keys := make([]string, 0, len(u.patch))
			for k := range u.patch {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Printf("  ~ %s (%s)\n", u.spec.Name, strings.Join(keys, ", "))
		}
		for _, p := range toRemove {
			fmt.Printf("  - %s\n", p.Name)
		}

		if dryRun {
			fmt.Println("\nDry run — no changes made.")
			return
		}

		fmt.Println()

		added, updated, removed := 0, 0, 0

		for _, spec := range toAdd {
			payload := map[string]interface{}{"name": spec.Name}
			if spec.Domain != "" {
				payload["domain"] = spec.Domain
			}
			if spec.Image != "" {
				payload["image"] = spec.Image
			}
			if spec.Repo != "" {
				payload["repo"] = spec.Repo
			}
			if spec.Branch != "" {
				payload["branch"] = spec.Branch
			}
			if spec.Port != 0 {
				payload["port"] = spec.Port
			}
			if spec.Static != "" {
				payload["static"] = spec.Static
			}
			if spec.CI != "" {
				ci, err := parseCIFlag(spec.CI)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  error: %s has invalid ci value: %v\n", spec.Name, err)
					continue
				}
				payload["ci"] = ci
			}
			var result map[string]interface{}
			if err := apiPost("/projects", payload, &result); err != nil {
				fmt.Fprintf(os.Stderr, "  error adding %s: %v\n", spec.Name, err)
				continue
			}
			fmt.Printf("  ✓ added %s\n", spec.Name)
			added++
		}

		for _, u := range toUpdate {
			var result map[string]interface{}
			if err := apiPatch("/projects/"+u.spec.Name, u.patch, &result); err != nil {
				fmt.Fprintf(os.Stderr, "  error updating %s: %v\n", u.spec.Name, err)
				continue
			}
			fmt.Printf("  ✓ updated %s\n", u.spec.Name)
			updated++
			if u.running {
				if err := apiPost("/projects/"+u.spec.Name+"/deploy", map[string]interface{}{}, nil); err != nil {
					fmt.Fprintf(os.Stderr, "  warning: redeploy for %s failed: %v\n", u.spec.Name, err)
				} else {
					fmt.Printf("  ✓ redeployed %s\n", u.spec.Name)
				}
			}
		}

		for _, p := range toRemove {
			if err := apiDelete("/projects/" + p.Name); err != nil {
				fmt.Fprintf(os.Stderr, "  error removing %s: %v\n", p.Name, err)
				continue
			}
			fmt.Printf("  ✓ removed %s\n", p.Name)
			removed++
		}

		fmt.Printf("\nDone. %d added, %d updated, %d removed.\n", added, updated, removed)
	},
}

func buildPatch(spec projectSpec, cur remoteProject) map[string]interface{} {
	patch := map[string]interface{}{}
	if spec.Domain != "" && spec.Domain != cur.Domain {
		patch["domain"] = spec.Domain
	}
	if spec.Image != "" && spec.Image != cur.Image {
		patch["image"] = spec.Image
	}
	if spec.Repo != "" && spec.Repo != cur.Repo {
		patch["repo"] = spec.Repo
	}
	if spec.Branch != "" && spec.Branch != cur.Branch {
		patch["branch"] = spec.Branch
	}
	if spec.Port != 0 && spec.Port != cur.Port {
		patch["port"] = spec.Port
	}
	if spec.Static != "" && spec.Static != cur.Static {
		patch["static"] = spec.Static
	}
	if spec.CI != "" {
		ci, err := parseCIFlag(spec.CI)
		if err == nil && ci != cur.CI {
			patch["ci"] = ci
		}
	}
	return patch
}

func parseProjectsFile(path string) (map[string]projectSpec, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	specs := map[string]projectSpec{}
	currentName := ""

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentName = strings.TrimSpace(line[1 : len(line)-1])
			if _, exists := specs[currentName]; !exists {
				specs[currentName] = projectSpec{Name: currentName}
			}
			continue
		}
		if currentName == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		spec := specs[currentName]
		switch k {
		case "domain":
			spec.Domain = v
		case "image":
			spec.Image = v
		case "repo":
			spec.Repo = v
		case "branch":
			spec.Branch = v
		case "port":
			if n, err := strconv.Atoi(v); err == nil {
				spec.Port = n
			}
		case "static":
			spec.Static = v
		case "ci":
			spec.CI = v
		}
		specs[currentName] = spec
	}
	return specs, scanner.Err()
}

func init() {
	rootCmd.AddCommand(applyCmd)
	applyCmd.Flags().StringP("file", "f", "poof.ini", "path to projects file")
	applyCmd.Flags().Bool("dry-run", false, "print plan without making changes")
	applyCmd.Flags().Bool("prune", false, "remove projects not in the file")
}
