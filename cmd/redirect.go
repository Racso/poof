package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var redirectCmd = &cobra.Command{
	Use:   "redirect",
	Short: "Manage domain redirects",
}

var redirectAddCmd = &cobra.Command{
	Use:   "add <from> <to>",
	Short: "Add a redirect (e.g. www.mysite.com mysite.com)",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		from, to := args[0], args[1]

		var result map[string]interface{}
		if err := apiPost("/redirects", map[string]string{"from": from, "to": to}, &result); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ redirect added: %s → https://%s\n", from, to)
	},
}

var redirectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all redirects",
	Run: func(cmd *cobra.Command, args []string) {
		var redirects []map[string]interface{}
		if err := apiGet("/redirects", &redirects); err != nil {
			fatal("%v", err)
		}

		if len(redirects) == 0 {
			fmt.Println("no redirects configured")
			return
		}

		fmt.Printf("%-6s %-40s %s\n", "ID", "FROM", "TO")
		fmt.Printf("%-6s %-40s %s\n", "--", "----", "--")
		for _, r := range redirects {
			id := int64(r["id"].(float64))
			from, _ := r["from"].(string)
			to, _ := r["to"].(string)
			fmt.Printf("%-6d %-40s %s\n", id, from, to)
		}
	},
}

var redirectDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a redirect by ID",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		id := args[0]

		if err := apiDelete("/redirects/" + id); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ redirect %s deleted\n", id)
	},
}

func init() {
	rootCmd.AddCommand(redirectCmd)
	redirectCmd.AddCommand(redirectAddCmd)
	redirectCmd.AddCommand(redirectListCmd)
	redirectCmd.AddCommand(redirectDeleteCmd)
}
