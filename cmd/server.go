package cmd

import (
	"fmt"
	"os"

	"github.com/racso/poof/config"
	"github.com/racso/poof/server"
	"github.com/racso/poof/store"
	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the Poof! daemon",
	Run: func(cmd *cobra.Command, args []string) {
		scfg, err := config.LoadServer()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
			os.Exit(1)
		}

		if scfg.Auth.Token == "" {
			fmt.Fprintln(os.Stderr, "error: auth.token must be set in config before starting the server")
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

		srv := server.New(scfg, st)
		if err := srv.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: server: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
