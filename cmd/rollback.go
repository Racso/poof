package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback <name>",
	Short: "Redeploy the previous successful image",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		var result map[string]interface{}
		if err := apiPost("/projects/"+name+"/rollback", map[string]interface{}{}, &result); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ rolled back %q\n", name)
		if img, ok := result["image"].(string); ok {
			fmt.Printf("  image: %s\n", img)
		}
	},
}

func init() {
	rootCmd.AddCommand(rollbackCmd)
}
