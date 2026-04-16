package cmd

import (
	"fmt"
	"os"
	"runtime"

	"io"

	"github.com/racso/poof/caddy"
	"github.com/racso/poof/config"
	"github.com/racso/poof/docker"
	gh "github.com/racso/poof/github"
	"github.com/racso/poof/server"
	"github.com/racso/poof/static"
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

		srv := server.New(scfg, st, newGitHubClient, dockerAdapter{}, staticAdapter{}, caddyAdapter{})
		if err := srv.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: server: %v\n", err)
			os.Exit(1)
		}
	},
}

func newGitHubClient(token string) server.RepoManager {
	return gh.NewClient(token)
}

// dockerAdapter delegates server.ContainerManager to the docker package functions.
type dockerAdapter struct{}

func (dockerAdapter) Deploy(cfg server.ContainerDeployConfig) error {
	return docker.Deploy(docker.DeployConfig{
		Name:          cfg.Name,
		Image:         cfg.Image,
		EnvVars:       cfg.EnvVars,
		Volumes:       cfg.Volumes,
		RegistryUser:  cfg.RegistryUser,
		RegistryToken: cfg.RegistryToken,
	})
}
func (dockerAdapter) Stop(name string) error              { return docker.Stop(name) }
func (dockerAdapter) IsRunning(name string) bool           { return docker.IsRunning(name) }
func (dockerAdapter) Logs(name string, lines int) (string, error) { return docker.Logs(name, lines) }

// staticAdapter delegates server.StaticDeployer to the static package functions.
type staticAdapter struct{}

func (staticAdapter) Deploy(dataDir, project string, depID int64, tarball io.Reader) error {
	return static.Deploy(dataDir, project, depID, tarball)
}
func (staticAdapter) Rollback(dataDir, project string, depID int64) error {
	return static.Rollback(dataDir, project, depID)
}
func (staticAdapter) IsDeployed(dataDir, project string) bool { return static.IsDeployed(dataDir, project) }
func (staticAdapter) Remove(dataDir, project string)          { static.Remove(dataDir, project) }

// caddyAdapter delegates server.CaddySyncer to the caddy package.
type caddyAdapter struct{}

func (caddyAdapter) Reload(adminURL, caddyfile string) error { return caddy.Reload(adminURL, caddyfile) }

func init() {
	rootCmd.AddCommand(serverCmd)
}
