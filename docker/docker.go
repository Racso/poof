package docker

import (
	"fmt"
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
// given image. Used by the self-update flow.
func PullSelf(image, user, token string) error {
	if user != "" && token != "" {
		if err := login(image, user, token); err != nil {
			return err
		}
	}
	out, err := exec.Command("docker", "pull", image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker pull failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
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
