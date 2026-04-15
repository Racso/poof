package cmd

import (
	"fmt"
	"os"
	"runtime"

	"github.com/racso/poof/config"
	gh "github.com/racso/poof/github"
	"github.com/racso/poof/server"
	"github.com/racso/poof/store"
	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the Poof! daemon",
	Run: func(cmd *cobra.Command, args []string) {
		if runtime.GOOS == "windows" {
			fatal("Poof! server requires Linux — Windows is supported for the CLI client only")
		}

		scfg, err := config.LoadServer()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
			os.Exit(1)
		}

		if scfg.Token == "" {
			fmt.Fprintln(os.Stderr, "error: token must be set in config before starting the server")
			os.Exit(1)
		}

		if err := os.MkdirAll(scfg.DataDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot create data dir %s: %v\n", scfg.DataDir, err)
			os.Exit(1)
		}

		st, err := store.Open(scfg.DBPath())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: open database: %v\n", err)
			os.Exit(1)
		}
		defer st.Close()

		srv := server.New(scfg, st, newGitHubClient)
		if err := srv.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: server: %v\n", err)
			os.Exit(1)
		}
	},
}

func newGitHubClient(token string) server.RepoManager {
	return gh.NewClient(token)
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
