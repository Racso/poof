package cmd

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

		checkCaddySetup(scfg)

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
func (dockerAdapter) GC(name, image string, keep, olderThanDays int, dryRun bool) (server.GCResult, error) {
	r, err := docker.GC(name, image, keep, olderThanDays, dryRun)
	return server.GCResult{Project: r.Project, Removed: r.Removed, Kept: r.Kept, Failed: r.Failed}, err
}
func (dockerAdapter) SweepOrphans(refs []string, dryRun bool) (server.GCResult, error) {
	r, err := docker.SweepOrphans(refs, dryRun)
	return server.GCResult{Project: r.Project, Removed: r.Removed, Kept: r.Kept, Failed: r.Failed}, err
}
func (dockerAdapter) PruneDangling() error { return docker.PruneDangling() }
func (dockerAdapter) ImagesDiskUsage() (int64, error) { return docker.ImagesDiskUsage() }

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
func (staticAdapter) GC(dataDir, project string, versions []server.StaticVersion, keep, olderThanDays int, dryRun bool) (server.GCResult, error) {
	sv := make([]static.VersionInfo, len(versions))
	for i, v := range versions {
		sv[i] = static.VersionInfo{DepID: v.DepID, DeployedAt: v.DeployedAt}
	}
	r, err := static.GC(dataDir, project, sv, keep, olderThanDays, dryRun)
	return server.GCResult{Project: r.Project, Removed: r.Removed, Kept: r.Kept, Failed: r.Failed}, err
}

// caddyAdapter delegates server.CaddySyncer to the caddy package.
type caddyAdapter struct{}

func (caddyAdapter) Reload(adminURL, caddyfile string) error { return caddy.Reload(adminURL, caddyfile) }

// checkCaddySetup inspects the Docker environment and prints warnings if the
// Caddy container is missing or lacks the shared static-files volume mount.
func checkCaddySetup(cfg *config.ServerConfig) {
	caddyName := caddyContainerName(cfg.CaddyAdminURL)
	if caddyName == "" {
		return // can't determine container name; skip checks
	}

	if !docker.ContainerExists(caddyName) {
		fmt.Fprintf(os.Stderr, "WARNING: Caddy container %q not found. Poof! won't be able to manage routing.\n", caddyName)
		fmt.Fprintf(os.Stderr, "  Ensure Caddy is running on the %q network with admin API enabled.\n", docker.NetworkName())
		return
	}

	staticMount := filepath.Join(cfg.DataDir, "static")
	if !docker.ContainerHasMount(caddyName, staticMount) {
		fmt.Fprintf(os.Stderr, "WARNING: Caddy container %q does not mount %s. Static sites will return 404.\n", caddyName, staticMount)
		fmt.Fprintf(os.Stderr, "  Add  -v %s:%s  to the Caddy container's volume mounts and recreate it.\n", staticMount, staticMount)
	}
}

// caddyContainerName extracts the hostname from the Caddy admin URL, which
// doubles as the container name on the Docker network.
func caddyContainerName(adminURL string) string {
	u, err := url.Parse(adminURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
