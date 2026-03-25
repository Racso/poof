package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var remoteUpdateCmd = &cobra.Command{
	Use:   "update-remote",
	Short: "Update the remote Poof! server to the latest image",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		var result map[string]string
		if err := apiPost("/update", nil, &result); err != nil {
			fatal("%v", err)
		}
		fmt.Println("Server updating — restarting Poof! now")
	},
}

func init() {
	rootCmd.AddCommand(remoteUpdateCmd)
}
