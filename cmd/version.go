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
		fmt.Printf("local:  commit=%s  built=%s\n", version.Commit, version.BuildTime)

		var info struct {
			Commit    string `json:"commit"`
			BuildTime string `json:"build_time"`
		}
		if err := apiGet("/version", &info); err != nil {
			fmt.Fprintf(os.Stderr, "server: %v\n", err)
			return
		}
		fmt.Printf("server: commit=%s  built=%s\n", info.Commit, info.BuildTime)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
