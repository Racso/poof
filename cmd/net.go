package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var netCmd = &cobra.Command{
	Use:   "net",
	Short: "Manage Docker networks and what is attached to them",
	Long: `Manage Poof-managed Docker networks and their members.

Each project's container runs isolated on its own network (poof-app-<name>,
shared only with Caddy for routing). A Poof-managed network lets you connect
things deliberately: several projects, a container Poof does not manage, Caddy,
or the daemon itself.

Membership is desired state — Poof re-applies it, so an attachment survives the
container being recreated, unlike a one-off 'docker network connect'.

Typical flow:
  poof net create backend --internal     # private network, no egress
  poof net add backend api worker        # attach both projects at once
  poof net show backend                  # see what is attached

Routing a domain to a container Poof does not manage:
  poof net create edge-myapp
  poof net add edge-myapp my-container --caddy`,
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
		fmt.Printf("\n\nAttach members: poof net add %s <member...> [--caddy] [--poof]\n", name)
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

Refuses if anything is still attached — detach members first with
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

// memberFlags builds the request body shared by add and remove.
func memberFlags(cmd *cobra.Command, members []string) map[string]interface{} {
	caddy, _ := cmd.Flags().GetBool("caddy")
	poof, _ := cmd.Flags().GetBool("poof")
	return map[string]interface{}{"members": members, "caddy": caddy, "poof": poof}
}

// looksLikeOldOrder reports whether the args look like the pre-v0.25 form
// `poof net add <project> <network>`, so we can say so instead of creating a
// membership with the arguments backwards.
func looksLikeOldOrder(args []string) bool {
	if len(args) != 2 {
		return false
	}
	var nets []map[string]interface{}
	if err := apiGet("/networks", &nets); err != nil {
		return false
	}
	isNet := func(name string) bool {
		for _, n := range nets {
			if s, _ := n["name"].(string); s == name {
				return true
			}
		}
		return false
	}
	return !isNet(args[0]) && isNet(args[1])
}

var netAddCmd = &cobra.Command{
	Use:   "add <network> [member...]",
	Short: "Attach projects, containers, Caddy or Poof to a network",
	Long: `Attach one or more members to a Poof-managed network.

A member is a Poof project or the name of a container Poof does not manage
(started by Compose, or by hand). Poof records the attachment as desired state
and re-applies it — so it survives the container being recreated, unlike a
one-off 'docker network connect'.

  --caddy   attach Caddy, so it can route to members of this network
  --poof    attach the Poof daemon, for members that call its API internally

Examples:
  poof net create backend --internal
  poof net add backend api worker          # two projects, one command
  poof net add edge-indigo my-app --caddy  # unmanaged container + Caddy
  poof net add admin my-tool --poof        # container that drives the Poof API`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		network := args[0]
		members := args[1:]

		if looksLikeOldOrder(args) {
			fatal("argument order changed: it is now 'poof net add <network> [member...]'.\n"+
				"  Did you mean: poof net add %s %s", args[1], args[0])
		}

		caddy, _ := cmd.Flags().GetBool("caddy")
		poof, _ := cmd.Flags().GetBool("poof")
		if len(members) == 0 && !caddy && !poof {
			fatal("specify at least one member, or --caddy / --poof")
		}

		var result map[string]interface{}
		if err := apiPost("/networks/"+network+"/members", memberFlags(cmd, members), &result); err != nil {
			fatal("%v", err)
		}

		for _, m := range members {
			fmt.Printf("✓ %q attached to %q\n", m, network)
		}
		if caddy {
			fmt.Printf("✓ Caddy attached to %q\n", network)
		}
		if poof {
			fmt.Printf("✓ Poof daemon attached to %q\n", network)
		}
	},
}

var netShowCmd = &cobra.Command{
	Use:   "show <network>",
	Short: "Show what is attached to a network",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		network := args[0]
		var members []map[string]interface{}
		if err := apiGet("/networks/"+network+"/members", &members); err != nil {
			fatal("%v", err)
		}
		if len(members) == 0 {
			fmt.Printf("nothing attached to network %q\n", network)
			return
		}
		fmt.Printf("%-24s  %s\n", "MEMBER", "KIND")
		for _, m := range members {
			name, _ := m["member"].(string)
			kind, _ := m["kind"].(string)
			fmt.Printf("%-24s  %s\n", name, kind)
		}
	},
}

var netListCmd = &cobra.Command{
	Use:   "list <project>",
	Short: "List networks a project is attached to",
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
		fmt.Println("NETWORK")
		for _, n := range nets {
			network, _ := n["network"].(string)
			fmt.Println(network)
		}
	},
}

var netRemoveCmd = &cobra.Command{
	Use:   "remove <network> [member...]",
	Short: "Detach members from a network",
	Long: `Detach one or more members from a Poof-managed network.

Takes effect immediately — the running container is detached, no redeploy
needed. Use --caddy / --poof to detach Caddy or the daemon.`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		network := args[0]
		members := args[1:]

		caddy, _ := cmd.Flags().GetBool("caddy")
		poof, _ := cmd.Flags().GetBool("poof")
		if len(members) == 0 && !caddy && !poof {
			fatal("specify at least one member, or --caddy / --poof")
		}

		var result map[string]interface{}
		if err := apiDeleteBody("/networks/"+network+"/members", memberFlags(cmd, members), &result); err != nil {
			fatal("%v", err)
		}

		removed, _ := result["removed"].([]interface{})
		if len(removed) == 0 {
			fmt.Printf("nothing to detach from %q\n", network)
			return
		}
		for _, m := range removed {
			fmt.Printf("✓ %v detached from %q\n", m, network)
		}
	},
}

func init() {
	rootCmd.AddCommand(netCmd)
	netCmd.AddCommand(netCreateCmd)
	netCmd.AddCommand(netLsCmd)
	netCmd.AddCommand(netDeleteCmd)
	netCmd.AddCommand(netAddCmd)
	netCmd.AddCommand(netShowCmd)
	netCmd.AddCommand(netListCmd)
	netCmd.AddCommand(netRemoveCmd)
	netCreateCmd.Flags().Bool("internal", false, "create an internal network (no external connectivity)")
	for _, c := range []*cobra.Command{netAddCmd, netRemoveCmd} {
		c.Flags().Bool("caddy", false, "include the Caddy container")
		c.Flags().Bool("poof", false, "include the Poof daemon")
	}
}
