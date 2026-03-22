package cmd

import (
	"fmt"

	"github.com/racso/poof/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show the client config file path",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(config.ClientConfigPath())
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
}
