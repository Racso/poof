package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Domain    string `toml:"domain"`
	APIPort   int    `toml:"api_port"`
	DataDir   string `toml:"data_dir"`
	PublicURL string `toml:"public_url"` // how the server is reachable from outside (set as POOF_URL secret)

	GitHub GitHubConfig `toml:"github"`
	Auth   AuthConfig   `toml:"auth"`
	Client ClientConfig `toml:"client"`
}

type GitHubConfig struct {
	User  string `toml:"user"`
	Token string `toml:"token"` // PAT with repo scope
}

type AuthConfig struct {
	Token string `toml:"token"` // global API token (CLI → server)
}

// ClientConfig is used when the binary runs as a CLI client pointing at a remote server.
type ClientConfig struct {
	Server string `toml:"server"` // e.g. "https://poof.rac.so:9000"
	Token  string `toml:"token"`
}

func defaults() Config {
	return Config{
		APIPort: 9000,
		DataDir: "/var/lib/poof",
	}
}

// Load reads config from the first file found, then applies env var overrides.
// Search order:
//  1. $POOF_CONFIG
//  2. /etc/poof/poof.toml
//  3. ~/.config/poof/poof.toml
func Load() (*Config, error) {
	cfg := defaults()

	path := configPath()
	if path != "" {
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
	}

	// Env var overrides
	if v := os.Getenv("POOF_SERVER"); v != "" {
		cfg.Client.Server = v
	}
	if v := os.Getenv("POOF_TOKEN"); v != "" {
		cfg.Client.Token = v
		cfg.Auth.Token = v
	}
	if v := os.Getenv("POOF_GITHUB_TOKEN"); v != "" {
		cfg.GitHub.Token = v
	}

	return &cfg, nil
}

func configPath() string {
	if v := os.Getenv("POOF_CONFIG"); v != "" {
		return v
	}
	if _, err := os.Stat("/etc/poof/poof.toml"); err == nil {
		return "/etc/poof/poof.toml"
	}
	home, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(home, ".config", "poof", "poof.toml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "poof.db")
}
