package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var removeCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a project and stop its container",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		yes, _ := cmd.Flags().GetBool("yes")

		// Fetch managed volumes to decide whether to prompt about host data.
		var vols []map[string]interface{}
		_ = apiGet("/projects/"+name+"/volumes", &vols)

		var managedVols []map[string]interface{}
		for _, v := range vols {
			if managed, ok := v["managed"].(bool); ok && managed {
				managedVols = append(managedVols, v)
			}
		}

		purge := false
		if len(managedVols) > 0 {
			dataPath := "/var/lib/poof/" + name
			var abort bool
			purge, abort = resolveDataIntent(cmd, dataPath)
			if abort {
				os.Exit(1)
			}
		}

		if !yes {
			fmt.Printf("Remove project %q? This will stop its container and clean up GitHub. [y/N] ", name)
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
				fmt.Println("aborted")
				return
			}
		}

		path := "/projects/" + name
		if purge {
			path += "?purge=true"
		}

		if err := apiDelete(path); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ project %q removed\n", name)
		if len(managedVols) == 0 {
			fmt.Printf("  no managed host data was left behind\n")
		} else if purge {
			fmt.Printf("  managed host data at /var/lib/poof/%s was deleted\n", name)
		} else {
			fmt.Printf("  managed host data at /var/lib/poof/%s was kept\n", name)
		}
	},
}

func init() {
	rootCmd.AddCommand(removeCmd)
	removeCmd.Flags().BoolP("yes", "y", false, "skip confirmation prompt")
	registerDataFlags(removeCmd)
}
