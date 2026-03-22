package docker

import (
	"encoding/json"
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
// If user and token are non-empty, it logs in to the registry first.
func PullSelf(user, token string) error {
	if user != "" && token != "" {
		if err := login(selfImage, user, token); err != nil {
			return err
		}
	}
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

// RecreateSelf replaces the running poof container with a fresh one using the
// newly pulled image, preserving all runtime config (volumes, networks, env,
// labels, restart policy).
//
// The key constraint: this goroutine runs inside the container it is replacing.
// Stopping the container first would kill this process before docker run could
// start the new one. Instead we rename the running container to free its name,
// start the new container, then stop the old one. By the time the stop signal
// kills this process, the new container is already running.
func RecreateSelf() error {
	oldName := selfContainer + "-old"

	// Clean up any leftover from a previous failed attempt.
	exec.Command("docker", "rm", "-f", oldName).Run()

	// Capture the running container's full config before touching anything.
	raw, err := exec.Command("docker", "inspect", selfContainer).Output()
	if err != nil {
		return fmt.Errorf("docker inspect failed: %w", err)
	}

	var info []struct {
		HostConfig struct {
			Binds         []string `json:"Binds"`
			RestartPolicy struct {
				Name string `json:"Name"`
			} `json:"RestartPolicy"`
		} `json:"HostConfig"`
		Config struct {
			Env    []string          `json:"Env"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		NetworkSettings struct {
			Networks map[string]json.RawMessage `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(raw, &info); err != nil || len(info) == 0 {
		return fmt.Errorf("could not parse container inspect output: %w", err)
	}
	c := info[0]

	// Rename running container to free the name for the new one.
	if out, err := exec.Command("docker", "rename", selfContainer, oldName).CombinedOutput(); err != nil {
		return fmt.Errorf("docker rename failed: %s", strings.TrimSpace(string(out)))
	}

	// Start the new container. Roll back the rename if this fails.
	args := []string{"run", "-d", "--name", selfContainer}
	if c.HostConfig.RestartPolicy.Name != "" {
		args = append(args, "--restart", c.HostConfig.RestartPolicy.Name)
	}
	for _, bind := range c.HostConfig.Binds {
		args = append(args, "-v", bind)
	}
	for network := range c.NetworkSettings.Networks {
		args = append(args, "--network", network)
	}
	for _, env := range c.Config.Env {
		args = append(args, "-e", env)
	}
	for k, v := range c.Config.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, selfImage)

	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		exec.Command("docker", "rename", oldName, selfContainer).Run()
		return fmt.Errorf("docker run failed: %s", strings.TrimSpace(string(out)))
	}

	// Stop and remove the old container. This kills this goroutine's process,
	// but the new container is already running by this point.
	exec.Command("docker", "stop", oldName).Run()
	exec.Command("docker", "rm", oldName).Run()
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
