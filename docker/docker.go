package docker

import (
	"fmt"
	"os/exec"
	"strings"
)

const networkName = "caddy-net"

type DeployConfig struct {
	Name    string
	Image   string
	Domain  string
	Port    int
	EnvVars map[string]string
}

// Pull pulls a Docker image from the registry.
func Pull(image string) error {
	out, err := exec.Command("docker", "pull", image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker pull failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Deploy pulls the image, stops any existing container for the project, and
// starts a new one with the appropriate Caddy labels.
func Deploy(cfg DeployConfig) error {
	if err := Pull(cfg.Image); err != nil {
		return err
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
		"--label", fmt.Sprintf("caddy=%s", cfg.Domain),
		"--label", fmt.Sprintf("caddy.reverse_proxy={{upstreams %d}}", cfg.Port),
	}

	for k, v := range cfg.EnvVars {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, cfg.Image)

	out, err := exec.Command("docker", args...).CombinedOutput()
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
