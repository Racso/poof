package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var deployCmd = &cobra.Command{
	Use:   "deploy <name>",
	Short: "Trigger a manual deploy (uses latest recorded image)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		image, _ := cmd.Flags().GetString("image")

		payload := map[string]interface{}{}
		if image != "" {
			payload["image"] = image
		}

		var result map[string]interface{}
		if err := apiPost("/projects/"+name+"/deploy", payload, &result); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ deployed %q\n", name)
		if d, ok := result["domain"].(string); ok {
			fmt.Printf("  https://%s\n", d)
		}
	},
}

func init() {
	rootCmd.AddCommand(deployCmd)
	deployCmd.Flags().String("image", "", "specific image to deploy (default: latest recorded)")
}
