package cmd

import (
	"fmt"
	"net/url"

	"github.com/racso/poof/config"
	"github.com/spf13/cobra"
)

var troubleshootCmd = &cobra.Command{
	Use:   "troubleshoot",
	Short: "Show troubleshooting help for server connectivity issues",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		host := serverHost()
		sshTarget := "root@" + host

		fmt.Printf(`Poof! server is unreachable. Here's what to check:

1. Verify your client config
   poof config    — shows the config file path
   cat <path>     — confirm the server URL and token look right

2. The server may be mid-restart
   If you recently ran ` + "`poof update-remote`" + `, it is likely just restarting.
   Wait a few seconds, then confirm with:
   poof version

3. Check the container status via SSH
   ssh %s
   docker ps | grep poof
   docker logs poof --tail 50

4. If the server failed to start after an update
   The previous image digest was saved on the server at:
     /var/lib/poof/rollback-image
   Use that digest to restore the previous version manually.

5. Force a clean redeploy via SSH
   (Paths below are examples — use your actual installation paths.)
   docker compose --env-file /opt/.env \
     -f /opt/docker/poof/docker-compose.yml pull
   docker compose --env-file /opt/.env \
     -f /opt/docker/poof/docker-compose.yml up -d
`, sshTarget)
	},
}

// serverHost extracts the hostname from the configured server URL.
// Falls back to the raw server string if it cannot be parsed.
func serverHost() string {
	raw := cfg.Server
	if raw == "" {
		raw = config.ClientConfigPath()
		return "<host>"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return raw
	}
	return u.Hostname()
}

func init() {
	rootCmd.AddCommand(troubleshootCmd)
}
