package docker

import (
	"fmt"
	"os/exec"
	"strings"
)

const networkName = "caddy-net"

const (
	selfImage     = "ghcr.io/racso/poof:latest"
	selfContainer = "poof"
)

// CurrentImageID returns the image SHA256 of the running poof container,
// or "" if it cannot be determined.
func CurrentImageID() string {
	out, err := exec.Command("docker", "inspect", "--format", "{{.Image}}", selfContainer).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// PullSelf pulls the latest poof image from the registry.
func PullSelf() error {
	out, err := exec.Command("docker", "pull", selfImage).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker pull failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// PreflightSelf runs `poof --version` in the new image as a sanity check.
func PreflightSelf() error {
	out, err := exec.Command("docker", "run", "--rm", selfImage, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// StopSelf stops the poof container. Docker's restart policy will bring it
// back automatically with the newly pulled image.
func StopSelf() error {
	out, err := exec.Command("docker", "stop", selfContainer).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker stop failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

type DeployConfig struct {
	Name          string
	Image         string
	Domain        string
	RootDomain    string // root domain for subpath routing (e.g. "rac.so")
	Port          int
	Subpath       string // disabled | redirect | proxy
	EnvVars       map[string]string
	RegistryUser  string // optional: login before pull
	RegistryToken string // optional: login before pull
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

// Deploy pulls the image, stops any existing container for the project, and
// starts a new one with the appropriate Caddy labels.
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
		"--label", fmt.Sprintf("caddy=%s", cfg.Domain),
		"--label", fmt.Sprintf("caddy.reverse_proxy={{upstreams %d}}", cfg.Port),
	}

	// Subpath routing: add a second Caddy server block on the root domain.
	if cfg.RootDomain != "" && cfg.Domain != cfg.RootDomain {
		switch cfg.Subpath {
		case "redirect":
			args = append(args,
				"--label", fmt.Sprintf("caddy_1=%s", cfg.RootDomain),
				"--label", fmt.Sprintf("caddy_1.handle_path=/%s/*", cfg.Name),
				"--label", fmt.Sprintf("caddy_1.handle_path.redir=https://%s{uri} 301", cfg.Domain),
			)
		case "proxy":
			args = append(args,
				"--label", fmt.Sprintf("caddy_1=%s", cfg.RootDomain),
				"--label", fmt.Sprintf("caddy_1.handle_path=/%s/*", cfg.Name),
				"--label", fmt.Sprintf("caddy_1.handle_path.reverse_proxy={{upstreams %d}}", cfg.Port),
			)
		}
	}

	for k, v := range cfg.EnvVars {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
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
