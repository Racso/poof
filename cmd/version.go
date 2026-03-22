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
		fmt.Printf("local:  commit=%s  committed=%s\n", version.Commit, version.CommitTime)

		var info struct {
			Commit     string `json:"commit"`
			CommitTime string `json:"commit_time"`
		}
		if err := apiGet("/version", &info); err != nil {
			fmt.Fprintf(os.Stderr, "server: %v\n", err)
			return
		}
		fmt.Printf("server: commit=%s  committed=%s\n", info.Commit, info.CommitTime)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
