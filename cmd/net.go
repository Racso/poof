package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var netCmd = &cobra.Command{
	Use:   "net",
	Short: "Manage Docker networks and attach them to projects",
	Long: `Manage Poof-managed Docker networks and attach them to projects.

A project's container is always on poof-net (for Caddy routing). Attaching an
extra network lets several projects talk to each other privately. Poof records
the attachment as desired state and re-applies it on every (re)deploy, so it
survives redeploys — unlike a one-off 'docker network connect'.

Typical flow:
  poof net create backend --internal     # private network, no egress
  poof net add api backend               # attach project 'api'
  poof net add worker backend            # attach project 'worker'
  poof deploy api && poof deploy worker   # re-create on the shared network`,
}

var netCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a Poof-managed Docker network",
	Long: `Create a Poof-managed Docker network.

With --internal the network has no external connectivity (no egress, nothing
published) — useful for backend-only traffic between containers.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		internal, _ := cmd.Flags().GetBool("internal")

		payload := map[string]interface{}{"name": name, "internal": internal}
		var result map[string]interface{}
		if err := apiPost("/networks", payload, &result); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ network %q created", name)
		if internal {
			fmt.Printf(" (internal)")
		}
		fmt.Printf("\n\nAttach it to a project: poof net add <project> %s\n", name)
	},
}

var netLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List Poof-managed Docker networks",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		var nets []map[string]interface{}
		if err := apiGet("/networks", &nets); err != nil {
			fatal("%v", err)
		}
		if len(nets) == 0 {
			fmt.Println("no networks")
			return
		}
		fmt.Printf("%-30s  %s\n", "NAME", "INTERNAL")
		for _, n := range nets {
			name, _ := n["name"].(string)
			internal := "no"
			if b, ok := n["internal"].(bool); ok && b {
				internal = "yes"
			}
			fmt.Printf("%-30s  %s\n", name, internal)
		}
	},
}

var netDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a Poof-managed network record",
	Long: `Delete a Poof-managed network record.

Refuses if any project is still attached — detach them first with
'poof net remove'. The underlying Docker network is left in place (it may hold
non-Poof endpoints); remove it with 'docker network rm <name>' if you want it
gone entirely.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		if err := apiDelete("/networks/" + name); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("✓ network %q removed\n", name)
	},
}

var netAddCmd = &cobra.Command{
	Use:   "add <project> <network>",
	Short: "Attach a network to a project",
	Long: `Attach a Poof-managed network to a project.

The network must already exist (see 'poof net create'). Changes take effect on
the next deploy.`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		project := args[0]
		network := args[1]

		payload := map[string]interface{}{"network": network}
		var result map[string]interface{}
		if err := apiPost("/projects/"+project+"/networks", payload, &result); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ %q attached to %q\n", network, project)
		fmt.Printf("\nRedeploy to apply: poof deploy %s\n", project)
	},
}

var netListCmd = &cobra.Command{
	Use:   "list <project>",
	Short: "List networks attached to a project",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		project := args[0]

		var nets []map[string]interface{}
		if err := apiGet("/projects/"+project+"/networks", &nets); err != nil {
			fatal("%v", err)
		}
		if len(nets) == 0 {
			fmt.Printf("no networks attached to project %q\n", project)
			return
		}
		fmt.Printf("%-4s  %s\n", "ID", "NETWORK")
		for _, n := range nets {
			id := fmt.Sprintf("%.0f", n["id"])
			network, _ := n["network"].(string)
			fmt.Printf("%-4s  %s\n", id, network)
		}
	},
}

var netRemoveCmd = &cobra.Command{
	Use:   "remove <project> <id>",
	Short: "Detach a network from a project",
	Long: `Detach a network from a project by its attachment ID (from 'poof net list').

Changes take effect on the next deploy.`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		project := args[0]
		id := args[1]

		if err := apiDelete("/projects/" + project + "/networks/" + id); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("✓ network attachment %s removed\n", id)
		fmt.Printf("\nRedeploy to apply: poof deploy %s\n", project)
	},
}

func init() {
	rootCmd.AddCommand(netCmd)
	netCmd.AddCommand(netCreateCmd)
	netCmd.AddCommand(netLsCmd)
	netCmd.AddCommand(netDeleteCmd)
	netCmd.AddCommand(netAddCmd)
	netCmd.AddCommand(netListCmd)
	netCmd.AddCommand(netRemoveCmd)
	netCreateCmd.Flags().Bool("internal", false, "create an internal network (no external connectivity)")
}
