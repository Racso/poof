package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// --- Server config ---

type ServerConfig struct {
	Domain    string `toml:"domain"`
	APIPort   int    `toml:"api_port"`
	DataDir   string `toml:"data_dir"`
	PublicURL string `toml:"public_url"` // how the server is reachable from outside

	GitHub GitHubConfig `toml:"github"`
	Auth   AuthConfig   `toml:"auth"`
}

type GitHubConfig struct {
	User  string `toml:"user"`
	Token string `toml:"token"` // PAT with repo scope
}

type AuthConfig struct {
	Token string `toml:"token"` // global API token (CLI → server)
}

func (c *ServerConfig) DBPath() string {
	return filepath.Join(c.DataDir, "poof.db")
}

// LoadServer reads config from the first server config file found, then applies
// env var overrides. Search order:
//  1. $POOF_CONFIG
//  2. /etc/poof/poof.toml
func LoadServer() (*ServerConfig, error) {
	cfg := &ServerConfig{
		APIPort: 9000,
		DataDir: "/var/lib/poof",
	}
	path := ServerConfigPath()
	if path != "" {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
	}
	if v := os.Getenv("POOF_GITHUB_TOKEN"); v != "" {
		cfg.GitHub.Token = v
	}
	return cfg, nil
}

// ServerConfigPath returns the path to the server config file, or "" if none found.
func ServerConfigPath() string {
	if v := os.Getenv("POOF_CONFIG"); v != "" {
		return v
	}
	if _, err := os.Stat("/etc/poof/poof.toml"); err == nil {
		return "/etc/poof/poof.toml"
	}
	return ""
}

// --- Client config ---

// ProfileEntry is a named connection profile in the client config.
type ProfileEntry struct {
	Server string `toml:"server"`
	Token  string `toml:"token"`
	Import string `toml:"import"` // path to a separate client config file to load
}

// ClientConfig holds connection settings for the CLI client.
// The default profile is at the top level (server + token).
// Named profiles live under [profiles.<name>].
type ClientConfig struct {
	Server   string                  `toml:"server"`
	Token    string                  `toml:"token"`
	Profiles map[string]ProfileEntry `toml:"profiles"`
}

// LoadClient reads the client config file, resolves the active profile, and
// applies env var overrides.
//
//   - profile:    named profile to use; "" means use the default (root-level fields)
//   - envProfile: if true, read profile name from $POOF_PROFILE (errors if unset)
func LoadClient(profile string, envProfile bool) (*ClientConfig, error) {
	if envProfile {
		p := os.Getenv("POOF_PROFILE")
		if p == "" {
			return nil, fmt.Errorf("--env-profile specified but $POOF_PROFILE is not set")
		}
		profile = p
	}

	path := ClientConfigPath()
	var file ClientConfig
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, &file); err != nil {
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
	}

	cfg, err := resolveProfile(file, profile)
	if err != nil {
		return nil, err
	}

	// Env var overrides (applied after profile resolution)
	if v := os.Getenv("POOF_SERVER"); v != "" {
		cfg.Server = v
	}
	if v := os.Getenv("POOF_TOKEN"); v != "" {
		cfg.Token = v
	}
	return cfg, nil
}

func resolveProfile(file ClientConfig, profile string) (*ClientConfig, error) {
	if profile == "" {
		return &ClientConfig{Server: file.Server, Token: file.Token}, nil
	}
	entry, ok := file.Profiles[profile]
	if !ok {
		return nil, fmt.Errorf("profile %q not found in config", profile)
	}
	if entry.Import != "" {
		importPath := expandTilde(entry.Import)
		var imported ClientConfig
		if _, err := toml.DecodeFile(importPath, &imported); err != nil {
			return nil, fmt.Errorf("importing profile %q from %s: %w", profile, importPath, err)
		}
		return &ClientConfig{Server: imported.Server, Token: imported.Token}, nil
	}
	return &ClientConfig{Server: entry.Server, Token: entry.Token}, nil
}

// ClientConfigPath returns the expected client config path regardless of whether
// the file exists. Used both for loading and for reporting to the user.
func ClientConfigPath() string {
	if v := os.Getenv("POOF_CONFIG"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "poof", "poof.toml")
}

func expandTilde(p string) string {
	if len(p) >= 2 && p[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
