package cmd

// update-remote is not yet implemented.
//
// # What we tried and why it failed
//
// The core challenge: the poof container must update itself, but every approach
// runs into the same wall — the process is inside the container it's replacing.
//
// Attempt 1 — docker stop, rely on restart:always
//   Docker treats `docker stop` as a manual stop and ignores restart:always.
//   Even when the daemon restarts the container, it uses the original image
//   digest, not the newly pulled :latest.
//
// Attempt 2 — docker compose up -d (paths from container labels)
//   The compose file and env file paths in the labels point to the HOST
//   filesystem. The poof container only mounts the Docker socket,
//   /var/lib/poof, and /etc/poof — so docker compose can't find the files.
//
// Attempt 3 — docker inspect → stop → run
//   Read config via docker inspect, stop the container, start a new one.
//   `docker stop poof` kills the goroutine's own process before `docker run`
//   can execute, leaving the server down with nothing to bring it back.
//
// Attempt 4 — docker rename → run → stop (not fully validated)
//   Rename the running container to free the name, start the new one, then
//   stop the old one. New container is up before the goroutine is killed.
//   Promising, but untested end-to-end and still complex.
//
// # Recommended approach for the next attempt
//
// Run the update logic OUTSIDE the container via a host-side script deployed
// by Ansible. POST /update pulls the image and writes a trigger file to
// /var/lib/poof/. A small script on the host watches for the trigger and runs
// `docker compose pull && up -d`. No self-kill problem, full file access, and
// the script never needs to be updated alongside poof itself.

import (
	"github.com/spf13/cobra"
)

var remoteUpdateCmd = &cobra.Command{
	Use:   "update-remote",
	Short: "Update the remote poof server to the latest image (not yet implemented)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fatal("update-remote is not yet implemented — see cmd/remote_update.go for history and next steps")
	},
}

func init() {
	rootCmd.AddCommand(remoteUpdateCmd)
}
