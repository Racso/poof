package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var configureCmd = &cobra.Command{
	Use:   "configure <name>",
	Short: "Update a project's configuration",
	Long: `Update one or more configuration fields of an existing project.

Only the flags you pass will be changed; omitted flags are left as-is.
The project token is never affected — GitHub Actions integrations remain valid.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		domain, _ := cmd.Flags().GetString("domain")
		image, _ := cmd.Flags().GetString("image")
		repo, _ := cmd.Flags().GetString("repo")
		branch, _ := cmd.Flags().GetString("branch")
		port, _ := cmd.Flags().GetInt("port")
		subpath, _ := cmd.Flags().GetString("subpath")
		folder, _ := cmd.Flags().GetString("folder")
		folderSet := cmd.Flags().Changed("folder")
		ciVal, _ := cmd.Flags().GetString("ci")
		ciSet := cmd.Flags().Changed("ci")

		staticOn := cmd.Flags().Changed("static")
		staticOff := cmd.Flags().Changed("no-static")
		spaOn := cmd.Flags().Changed("spa")
		spaOff := cmd.Flags().Changed("no-spa")
		buildOn := cmd.Flags().Changed("build")
		buildOff := cmd.Flags().Changed("no-build")

		if staticOn && staticOff {
			fatal("cannot use --static and --no-static together")
		}
		if spaOn && spaOff {
			fatal("cannot use --spa and --no-spa together")
		}
		if buildOn && buildOff {
			fatal("cannot use --build and --no-build together")
		}

		payload := map[string]interface{}{}
		if domain != "" {
			payload["domain"] = domain
		}
		if image != "" {
			payload["image"] = image
		}
		if repo != "" {
			payload["repo"] = repo
		}
		if branch != "" {
			payload["branch"] = branch
		}
		if port != 0 {
			payload["port"] = port
		}
		if subpath != "" {
			payload["subpath"] = subpath
		}
		if folderSet {
			payload["folder"] = folder // allows clearing with --folder ""
		}
		if spaOn {
			payload["static"] = "spa"
		} else if spaOff {
			payload["static"] = "static"
		} else if staticOn {
			payload["static"] = "static"
		} else if staticOff {
			payload["static"] = "container"
		}
		if buildOn {
			payload["build"] = true
		} else if buildOff {
			payload["build"] = false
		}
		if ciSet {
			ci, err := parseCIFlag(ciVal)
			if err != nil {
				fatal("%v", err)
			}
			payload["ci"] = ci
		}

		if len(payload) == 0 {
			fatal("no fields to update — pass at least one flag")
		}

		var result map[string]interface{}
		if err := apiPatch("/projects/"+name, payload, &result); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ project %q updated\n", name)
		if d, ok := result["domain"].(string); ok {
			fmt.Printf("  domain:  %s\n", d)
		}
		if i, ok := result["image"].(string); ok {
			fmt.Printf("  image:   %s\n", i)
		}
		if r, ok := result["repo"].(string); ok {
			fmt.Printf("  repo:    %s\n", r)
		}
		if b, ok := result["branch"].(string); ok {
			fmt.Printf("  branch:  %s\n", b)
		}
		if f, ok := result["folder"].(string); ok && f != "" {
			fmt.Printf("  folder:  %s\n", f)
		}
	},
}

func init() {
	rootCmd.AddCommand(configureCmd)
	configureCmd.Flags().String("domain", "", "new domain")
	configureCmd.Flags().String("image", "", "new Docker image")
	configureCmd.Flags().String("repo", "", "new GitHub repo (owner/name)")
	configureCmd.Flags().String("branch", "", "new branch to deploy")
	configureCmd.Flags().Int("port", 0, "new container port")
	configureCmd.Flags().String("subpath", "", "new subpath routing mode: disabled, redirect, or proxy")
	configureCmd.Flags().String("folder", "", "repo subfolder containing the Dockerfile (use \"\" to clear)")
	configureCmd.Flags().Bool("static", false, "convert to a static site served by Caddy")
	configureCmd.Flags().Bool("no-static", false, "revert from static to a container project")
	configureCmd.Flags().Bool("spa", false, "enable SPA mode with try_files fallback")
	configureCmd.Flags().Bool("no-spa", false, "disable SPA mode (revert to plain static)")
	configureCmd.Flags().Bool("build", false, "use Dockerfile to build static files")
	configureCmd.Flags().Bool("no-build", false, "disable Dockerfile build for static files")
	configureCmd.Flags().String("ci", "", "enable/disable automatic CI workflow setup (yes/no)")
}
