package docker

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// networkName is the control-plane network: Caddy and the Poof daemon only.
// Project containers do NOT live here — each gets its own per-app network.
const networkName = "poof-net"

type DeployConfig struct {
	Name          string
	Image         string
	EnvVars       map[string]string
	Volumes       []string // host:container mount specs
	Network       string   // primary Docker network (the project's own per-app net)
	Networks      []string // extra Docker networks to attach (besides the primary)
	RegistryUser  string   // optional: login before pull
	RegistryToken string   // optional: login before pull
	CreateOnly    bool     // create the container without starting it (paused project)
}

// managedNetworkLabel marks Docker networks created by Poof so `poof net ls`
// can distinguish them from networks created out-of-band.
const managedNetworkLabel = "poof.managed=true"

// registryHost extracts the registry hostname from an image reference.
// "ghcr.io/foo/bar:tag" → "ghcr.io"; "ubuntu:22.04" → "" (Docker Hub).
func registryHost(image string) string {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) > 1 && strings.ContainsAny(parts[0], ".:") {
		return parts[0]
	}
	return ""
}

// login authenticates with the registry that hosts the given image.
func login(image, user, token string) error {
	registry := registryHost(image)
	args := []string{"login", "-u", user, "--password-stdin"}
	if registry != "" {
		args = append(args, registry)
	}
	cmd := exec.Command("docker", args...)
	cmd.Stdin = strings.NewReader(token)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("registry login failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// PullSelf logs in to the registry (if credentials are provided) and pulls the
// given image. Returns the pull output (including Status and Digest lines) for
// logging by the caller. Used by the self-update flow.
func PullSelf(image, user, token string) (string, error) {
	if user != "" && token != "" {
		if err := login(image, user, token); err != nil {
			return "", err
		}
	}
	out, err := exec.Command("docker", "pull", image).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker pull failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// SelfContainerName returns the Docker container name of the running process
// by inspecting the container whose ID matches the current hostname (Docker
// sets the short container ID as the hostname by default).
func SelfContainerName() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("get hostname: %w", err)
	}
	out, err := exec.Command("docker", "inspect", "--format", "{{.Name}}", hostname).Output()
	if err != nil {
		return "", fmt.Errorf("docker inspect self: %w", err)
	}
	name := strings.TrimPrefix(strings.TrimSpace(string(out)), "/")
	if name == "" {
		return "", fmt.Errorf("empty container name")
	}
	return name, nil
}

// ReplaceSelf inspects the named container's full configuration (mounts,
// networks, env vars, restart policy) and launches a disposable helper
// container that will — after we exit — stop the current container, remove
// it, and start a fresh one with newImage preserving the original config.
func ReplaceSelf(containerName, newImage string) error {
	type inspectResult struct {
		HostConfig struct {
			Binds         []string
			RestartPolicy struct{ Name string }
		}
		NetworkSettings struct {
			Networks map[string]json.RawMessage
		}
		Config struct {
			Env []string
		}
	}

	raw, err := exec.Command("docker", "inspect", containerName).Output()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", containerName, err)
	}
	var results []inspectResult
	if err := json.Unmarshal(raw, &results); err != nil || len(results) == 0 {
		return fmt.Errorf("parse inspect: %w", err)
	}
	cfg := results[0]

	runArgs := []string{"run", "-d", "--name", containerName}
	if policy := cfg.HostConfig.RestartPolicy.Name; policy != "" && policy != "no" {
		runArgs = append(runArgs, "--restart", policy)
	}
	for network := range cfg.NetworkSettings.Networks {
		runArgs = append(runArgs, "--network", network)
	}
	for _, bind := range cfg.HostConfig.Binds {
		runArgs = append(runArgs, "-v", shellQuote(bind))
	}
	for _, env := range cfg.Config.Env {
		runArgs = append(runArgs, "-e", shellQuote(env))
	}
	runArgs = append(runArgs, newImage)

	script := fmt.Sprintf(
		"sleep 2 && docker stop %s && docker rm %s && docker %s",
		containerName, containerName, strings.Join(runArgs, " "),
	)
	out, err := exec.Command("docker", "run", "--rm", "-d",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"docker:27-cli",
		"sh", "-c", script,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launch helper: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// InspectLabels returns the OCI labels of a local image.
func InspectLabels(image string) map[string]string {
	out, err := exec.Command(
		"docker", "inspect", "--format",
		`{{range $k,$v := .Config.Labels}}{{$k}}={{$v}}{{"\n"}}{{end}}`,
		image,
	).Output()
	if err != nil {
		return nil
	}
	labels := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			labels[k] = v
		}
	}
	return labels
}

// Deploy pulls the image, stops any existing container for the project, and
// starts a new one on the project's own network (cfg.Network). The caller is
// responsible for ensuring the network exists.
func Deploy(cfg DeployConfig) error {
	if cfg.Network == "" {
		return fmt.Errorf("deploy %s: no network specified", cfg.Name)
	}
	if cfg.RegistryUser != "" && cfg.RegistryToken != "" {
		if err := login(cfg.Image, cfg.RegistryUser, cfg.RegistryToken); err != nil {
			return err
		}
	}

	out, err := exec.Command("docker", "pull", cfg.Image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker pull failed: %s", strings.TrimSpace(string(out)))
	}
	if pullOut := strings.TrimSpace(string(out)); pullOut != "" {
		log.Printf("deploy %s: pull output: %s", cfg.Name, pullOut)
	}

	containerName := containerFor(cfg.Name)

	// Stop and remove any existing container.
	if stopOut, stopErr := exec.Command("docker", "stop", containerName).CombinedOutput(); stopErr != nil {
		msg := strings.TrimSpace(string(stopOut))
		if !strings.Contains(msg, "No such container") {
			log.Printf("deploy %s: stop %s: %s", cfg.Name, containerName, msg)
		}
	}
	if rmOut, rmErr := exec.Command("docker", "rm", containerName).CombinedOutput(); rmErr != nil {
		msg := strings.TrimSpace(string(rmOut))
		if !strings.Contains(msg, "No such container") {
			log.Printf("deploy %s: rm %s: %s", cfg.Name, containerName, msg)
		}
	}

	// Create + start instead of `run` so a paused project can stage the new
	// container without starting it. Restart policy is `no` while paused (a
	// daemon restart must not revive a paused workload); resume flips it back.
	restart := "always"
	if cfg.CreateOnly {
		restart = "no"
	}
	args := []string{
		"create",
		"--name", containerName,
		"--network", cfg.Network,
		"--restart", restart,
	}

	// Attach any additional project networks. Poof re-applies these on every
	// (re)create, so the membership survives redeploys — unlike a one-off
	// `docker network connect`.
	for _, net := range cfg.Networks {
		if net == "" || net == cfg.Network {
			continue
		}
		args = append(args, "--network", net)
	}

	for k, v := range cfg.EnvVars {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	for _, mount := range cfg.Volumes {
		args = append(args, "-v", mount)
	}

	args = append(args, cfg.Image)

	out, err = exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker create failed: %s", strings.TrimSpace(string(out)))
	}
	containerID := strings.TrimSpace(string(out))

	if cfg.CreateOnly {
		log.Printf("deploy %s: container created (not started): %s", cfg.Name, containerID)
		return nil
	}

	if out, err := exec.Command("docker", "start", containerName).CombinedOutput(); err != nil {
		return fmt.Errorf("docker start failed: %s", strings.TrimSpace(string(out)))
	}
	log.Printf("deploy %s: container started: %s", cfg.Name, containerID)
	return nil
}

// Stop stops and removes the container for a project.
func Stop(projectName string) error {
	containerName := containerFor(projectName)
	exec.Command("docker", "stop", containerName).Run()
	out, err := exec.Command("docker", "rm", containerName).CombinedOutput()
	if err != nil {
		// If the container didn't exist, that's fine.
		if strings.Contains(string(out), "No such container") {
			return nil
		}
		return fmt.Errorf("docker rm failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Suspend stops a project's container without removing it, so its writable
// layer survives for investigation and Start can bring back the identical
// container. The restart policy is set to `no` first: with `always`, a
// manually stopped container is revived when the Docker daemon restarts,
// which would silently un-pause the workload on a server reboot.
// A missing container is a no-op (paused project that was never deployed).
func Suspend(projectName string) error {
	containerName := containerFor(projectName)
	if out, err := exec.Command("docker", "update", "--restart=no", containerName).CombinedOutput(); err != nil {
		if strings.Contains(string(out), "No such container") {
			return nil
		}
		return fmt.Errorf("docker update failed: %s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("docker", "stop", containerName).CombinedOutput(); err != nil {
		return fmt.Errorf("docker stop failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Start starts a project's stopped or created container and restores the
// `always` restart policy. A missing container is a no-op (paused project
// that was never deployed).
func Start(projectName string) error {
	containerName := containerFor(projectName)
	if out, err := exec.Command("docker", "update", "--restart=always", containerName).CombinedOutput(); err != nil {
		if strings.Contains(string(out), "No such container") {
			return nil
		}
		return fmt.Errorf("docker update failed: %s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("docker", "start", containerName).CombinedOutput(); err != nil {
		return fmt.Errorf("docker start failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// SnapshotResult describes a forensic snapshot of a project's container.
type SnapshotResult struct {
	ImageRef string `json:"image"`
	Dir      string `json:"dir"`
}

// Snapshot preserves a project's container for forensics: the writable layer
// is committed to a local image (poof-snapshot/<project>:<timestamp>) and the
// container's logs and full inspect output are dumped under
// <dataDir>/snapshots/. Works on stopped containers and does not disturb the
// container. Snapshot images use their own repository name and are never
// recorded as deployments, so GC leaves them alone.
func Snapshot(projectName, dataDir string) (*SnapshotResult, error) {
	containerName := containerFor(projectName)
	ts := time.Now().UTC().Format("20060102-150405")
	ref := fmt.Sprintf("poof-snapshot/%s:%s", projectName, ts)

	out, err := exec.Command("docker", "commit", containerName, ref).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker commit failed: %s", strings.TrimSpace(string(out)))
	}

	dir := filepath.Join(dataDir, "snapshots", projectName+"-"+ts)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("snapshot image %s created, but snapshot dir failed: %w", ref, err)
	}

	logs, err := exec.Command("docker", "logs", containerName).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("snapshot image %s created, but docker logs failed: %s", ref, strings.TrimSpace(string(logs)))
	}
	if err := os.WriteFile(filepath.Join(dir, "logs.txt"), logs, 0600); err != nil {
		return nil, fmt.Errorf("snapshot image %s created, but writing logs failed: %w", ref, err)
	}

	// Inspect output includes the container's env vars (secrets) — 0600.
	inspect, err := exec.Command("docker", "inspect", containerName).Output()
	if err != nil {
		return nil, fmt.Errorf("snapshot image %s created, but docker inspect failed: %w", ref, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inspect.json"), inspect, 0600); err != nil {
		return nil, fmt.Errorf("snapshot image %s created, but writing inspect failed: %w", ref, err)
	}

	return &SnapshotResult{ImageRef: ref, Dir: dir}, nil
}

// Logs returns the last n log lines from the project's container.
func Logs(projectName string, lines int) (string, error) {
	containerName := containerFor(projectName)
	out, err := exec.Command(
		"docker", "logs", "--tail", fmt.Sprintf("%d", lines), containerName,
	).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker logs failed: %s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// IsRunning returns true if the container for a project is running.
func IsRunning(projectName string) bool {
	out, err := exec.Command(
		"docker", "inspect", "-f", "{{.State.Running}}", containerFor(projectName),
	).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

func containerFor(projectName string) string {
	return "poof-" + projectName
}

// NetworkName returns the control-plane Docker network name (Caddy + daemon).
func NetworkName() string { return networkName }

// ContainerName returns the Docker container name Poof uses for a project.
func ContainerName(projectName string) string { return containerFor(projectName) }

// ConnectNetwork attaches a container to a network. Idempotent: a container
// already on the network is treated as success.
func ConnectNetwork(network, container string) error {
	out, err := exec.Command("docker", "network", "connect", network, container).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "already exists in network") || strings.Contains(msg, "already connected") {
			return nil
		}
		return fmt.Errorf("docker network connect %s %s: %s", network, container, msg)
	}
	return nil
}

// DisconnectNetwork detaches a container from a network. Idempotent: a
// container not on the network (or absent) is treated as success.
func DisconnectNetwork(network, container string) error {
	out, err := exec.Command("docker", "network", "disconnect", network, container).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "is not connected") || strings.Contains(msg, "No such container") || strings.Contains(msg, "No such network") {
			return nil
		}
		return fmt.Errorf("docker network disconnect %s %s: %s", network, container, msg)
	}
	return nil
}

// RemoveNetwork removes a Docker network. A network that doesn't exist is
// treated as success; a network with active endpoints is an error (the caller
// decides whether that's fatal — hand-attached containers keep a net alive).
func RemoveNetwork(name string) error {
	out, err := exec.Command("docker", "network", "rm", name).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "not found") || strings.Contains(msg, "No such network") {
			return nil
		}
		return fmt.Errorf("docker network rm %s: %s", name, msg)
	}
	return nil
}

// NetworkExists reports whether a Docker network with the given name exists.
func NetworkExists(name string) bool {
	err := exec.Command("docker", "network", "inspect", name).Run()
	return err == nil
}

// CreateNetwork creates a Docker network labelled as Poof-managed. If internal
// is true the network has no external connectivity (no egress, not published).
// It is an error if the network already exists.
func CreateNetwork(name string, internal bool) error {
	args := []string{"network", "create", "--label", managedNetworkLabel}
	if internal {
		args = append(args, "--internal")
	}
	args = append(args, name)
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker network create failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// EnsureNetwork creates the network if it does not already exist. Idempotent —
// safe to call on every deploy so a project's networks survive even if one was
// removed out-of-band.
func EnsureNetwork(name string, internal bool) error {
	if NetworkExists(name) {
		return nil
	}
	return CreateNetwork(name, internal)
}

// ContainerExists reports whether a container with the given name exists
// (running or stopped).
func ContainerExists(name string) bool {
	err := exec.Command("docker", "inspect", "--format", "{{.Id}}", name).Run()
	return err == nil
}

// ContainerHasMount reports whether the given container has a bind mount
// whose destination (container path) matches the specified path.
func ContainerHasMount(containerName, mountDest string) bool {
	out, err := exec.Command(
		"docker", "inspect", "--format",
		`{{range .Mounts}}{{.Destination}}{{"\n"}}{{end}}`,
		containerName,
	).Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == mountDest {
			return true
		}
	}
	return false
}
