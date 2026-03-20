package cmd

import (
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show container logs",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		lines, _ := cmd.Flags().GetInt("lines")

		url := fmt.Sprintf("%s/projects/%s/logs?lines=%d", serverURL(), name, lines)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			fatal("%v", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiToken())

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fatal("could not reach poof server: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			fatal("%s", string(body))
		}

		io.Copy(cmd.OutOrStdout(), resp.Body)
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
	logsCmd.Flags().IntP("lines", "n", 100, "number of log lines to show")
}
