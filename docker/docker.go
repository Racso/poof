package docker

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const networkName = "caddy-net"

type DeployConfig struct {
	Name          string
	Image         string
	EnvVars       map[string]string
	Volumes       []string // host:container mount specs
	RegistryUser  string   // optional: login before pull
	RegistryToken string   // optional: login before pull
}

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
// starts a new one on the caddy-net network.
func Deploy(cfg DeployConfig) error {
	if cfg.RegistryUser != "" && cfg.RegistryToken != "" {
		if err := login(cfg.Image, cfg.RegistryUser, cfg.RegistryToken); err != nil {
			return err
		}
	}

	out, err := exec.Command("docker", "pull", cfg.Image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker pull failed: %s", strings.TrimSpace(string(out)))
	}

	containerName := containerFor(cfg.Name)

	// Stop and remove any existing container — ignore errors (may not exist).
	exec.Command("docker", "stop", containerName).Run()
	exec.Command("docker", "rm", containerName).Run()

	args := []string{
		"run", "-d",
		"--name", containerName,
		"--network", networkName,
		"--restart", "always",
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
		return fmt.Errorf("docker run failed: %s", strings.TrimSpace(string(out)))
	}
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
