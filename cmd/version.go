package cmd

import (
	"fmt"
	"os"

	"github.com/racso/poof/version"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show local build info and remote server version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("local:  Poof! %s\n", version.String())

		var info struct {
			Number     string `json:"number"`
			Commit     string `json:"commit"`
			CommitTime string `json:"commit_time"`
		}
		if err := apiGet("/version", &info); err != nil {
			fmt.Fprintf(os.Stderr, "server: %v\n", err)
			return
		}
		serverVersion := "v" + info.Number + "   // " + info.Commit + " @ " + info.CommitTime
		fmt.Printf("server: Poof! %s\n", serverVersion)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
