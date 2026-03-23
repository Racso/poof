package caddy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/racso/poof/store"
)

// GenerateCaddyfile builds a complete Caddyfile from the given projects and
// redirects. Only pass projects whose containers are currently running.
// rootDomain is used to generate subpath routing blocks on the root site.
func GenerateCaddyfile(projects []store.Project, redirects []store.Redirect, rootDomain string) string {
	var b strings.Builder

	// subpathLines collects handle_path directives grouped by root domain.
	subpathLines := map[string][]string{}

	for _, p := range projects {
		fmt.Fprintf(&b, "%s {\n\treverse_proxy poof-%s:%d\n}\n\n", p.Domain, p.Name, p.Port)

		if rootDomain != "" && p.Domain != rootDomain && p.Subpath != "disabled" {
			switch p.Subpath {
			case "redirect":
				subpathLines[rootDomain] = append(subpathLines[rootDomain],
					fmt.Sprintf("\thandle_path /%s/* {\n\t\tredir https://%s{uri} 301\n\t}", p.Name, p.Domain))
			case "proxy":
				subpathLines[rootDomain] = append(subpathLines[rootDomain],
					fmt.Sprintf("\thandle_path /%s/* {\n\t\treverse_proxy poof-%s:%d\n\t}", p.Name, p.Name, p.Port))
			}
		}
	}

	for domain, lines := range subpathLines {
		fmt.Fprintf(&b, "%s {\n%s\n}\n\n", domain, strings.Join(lines, "\n"))
	}

	for _, r := range redirects {
		fmt.Fprintf(&b, "%s {\n\tredir https://%s{uri} 301\n}\n\n", r.FromDomain, r.ToDomain)
	}

	return b.String()
}

// Reload posts the generated Caddyfile to the Caddy admin API for a
// zero-downtime config reload.
func Reload(adminURL, caddyfile string) error {
	url := strings.TrimRight(adminURL, "/") + "/load"
	resp, err := http.Post(url, "text/caddyfile", bytes.NewBufferString(caddyfile))
	if err != nil {
		return fmt.Errorf("caddy reload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy reload: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
