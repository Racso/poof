package caddy

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/racso/poof/store"
)

// WriteRedirects serializes the given redirects as a Caddyfile and writes it
// to path. An empty list produces an empty file (valid for Caddy's import).
func WriteRedirects(path string, redirects []store.Redirect) error {
	var b strings.Builder
	for _, r := range redirects {
		fmt.Fprintf(&b, "%s {\n\tredir https://%s{uri} 301\n}\n", r.FromDomain, r.ToDomain)
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

// Reload restarts the named Caddy container so caddy-docker-proxy regenerates
// its configuration (picking up the updated redirects file).
func Reload(container string) error {
	out, err := exec.Command("docker", "restart", container).CombinedOutput()
	if err != nil {
		return fmt.Errorf("caddy reload failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
