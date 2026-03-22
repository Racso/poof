package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var remoteUpdateCmd = &cobra.Command{
	Use:   "update-remote",
	Short: "Update the remote poof server to the latest image",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Pulling new image and running pre-flight check on the server...")
		var result struct {
			Status string `json:"status"`
		}
		if err := apiPost("/update", nil, &result); err != nil {
			fatal("%v", err)
		}
		fmt.Println(result.Status)
	},
}

func init() {
	rootCmd.AddCommand(remoteUpdateCmd)
}
