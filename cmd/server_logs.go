package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

var serverLogsLines int

var serverLogsCmd = &cobra.Command{
	Use:   "server-logs",
	Short: "Show the poof server's own logs",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		url := fmt.Sprintf("%s/logs/server?lines=%d", serverURL(), serverLogsLines)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			fatal("%v", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiToken())

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fatal("could not reach poof server at %s: %v", serverURL(), err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			fatal("server returned %s", resp.Status)
		}
		switch resp.Header.Get("X-Poof-Log-Source") {
		case "file":
			fmt.Fprintln(os.Stderr, "source: log file")
		case "docker":
			fmt.Fprintln(os.Stderr, "source: docker logs (log file unavailable)")
		case "none", "":
			fmt.Fprintln(os.Stderr, "source: (no logs available)")
		}
		io.Copy(os.Stdout, resp.Body)
	},
}

func init() {
	serverLogsCmd.Flags().IntVarP(&serverLogsLines, "lines", "n", 100, "number of lines to show")
	rootCmd.AddCommand(serverLogsCmd)
}
