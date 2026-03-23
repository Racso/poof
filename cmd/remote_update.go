package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var remoteUpdateCmd = &cobra.Command{
	Use:   "update-remote",
	Short: "Update the remote Poof! server to the latest release",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		var result struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		}
		if err := apiPost("/update", nil, &result); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("Server updating to %s — restarting now\n", result.Version)
	},
}

func init() {
	rootCmd.AddCommand(remoteUpdateCmd)
}
