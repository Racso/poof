package store

import (
	"fmt"
	"strings"
)

// ContainerPrefix is the prefix Poof puts on every project's container name
// (project "web" → container "poof-web"). Caddy reaches containers by this
// name over the project's Docker network, so it must be resolvable.
const ContainerPrefix = "poof-"

// ValidateProjectName reports whether a project name is safe to register.
//
// The binding constraint is DNS: Caddy dials the container by name
// ("poof-<project>"), and Go's resolver enforces RFC 1123 hostname rules
// strictly — it rejects a malformed name outright rather than querying for
// it. Docker's embedded DNS is more permissive, so an invalid name resolves
// fine with `nslookup` from inside a container while every proxied request
// fails with a 502. That divergence makes the breakage hard to diagnose,
// which is exactly why it's worth rejecting up front.
//
// Concretely: a project named ".z0" yields container "poof-.z0", whose first
// DNS label is "poof-" — and a label may not end in a hyphen.
func ValidateProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("project name cannot be empty")
	}
	if strings.ContainsAny(name, " \t\n/\\:@") {
		return fmt.Errorf("project name %q contains an invalid character (no spaces, slashes, colons or @)", name)
	}

	host := ContainerPrefix + name
	if len(host) > 253 {
		return fmt.Errorf("project name %q is too long (container name would exceed 253 characters)", name)
	}

	for _, label := range strings.Split(host, ".") {
		if err := validateDNSLabel(label); err != nil {
			return fmt.Errorf("project name %q is not usable: container name %q %w", name, host, err)
		}
	}
	return nil
}

// validateDNSLabel checks a single DNS label against RFC 1123: 1–63
// characters, alphanumeric at both ends, hyphens allowed only in between.
func validateDNSLabel(label string) error {
	if label == "" {
		return fmt.Errorf("has an empty part (consecutive or leading/trailing dots)")
	}
	if len(label) > 63 {
		return fmt.Errorf("has a part longer than 63 characters (%q)", label)
	}
	if !isAlphanumeric(label[0]) {
		return fmt.Errorf("has a part that does not start with a letter or digit (%q)", label)
	}
	if !isAlphanumeric(label[len(label)-1]) {
		return fmt.Errorf("has a part that does not end with a letter or digit (%q)", label)
	}
	for i := 0; i < len(label); i++ {
		if !isAlphanumeric(label[i]) && label[i] != '-' {
			return fmt.Errorf("has a part with an invalid character (%q)", label)
		}
	}
	return nil
}

// ValidateWildcardZone reports whether zone can be served by a *.<zone>
// certificate. It must be a hostname of at least two labels: a wildcard
// directly under a top-level domain ("*.com") is refused by every CA.
func ValidateWildcardZone(zone string) error {
	if zone == "" {
		return fmt.Errorf("zone cannot be empty")
	}
	if strings.Contains(zone, "*") {
		return fmt.Errorf("zone %q must not contain '*': pass the zone itself (e.g. rac.so), the wildcard is implied", zone)
	}
	if len(zone) > 253 {
		return fmt.Errorf("zone %q is too long", zone)
	}
	labels := strings.Split(zone, ".")
	if len(labels) < 2 {
		return fmt.Errorf("zone %q needs at least two labels (e.g. example.com)", zone)
	}
	for _, label := range labels {
		if err := validateDNSLabel(label); err != nil {
			return fmt.Errorf("zone %q %w", zone, err)
		}
	}
	return nil
}

// ValidateDNSProvider checks the name of a Caddy DNS provider module
// (dns.providers.<name>). It is written into the Caddyfile verbatim, so only
// the characters module names use are accepted.
func ValidateDNSProvider(name string) error {
	if name == "" {
		return fmt.Errorf("DNS provider cannot be empty")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' {
			return fmt.Errorf("DNS provider %q may only contain lowercase letters, digits and underscores", name)
		}
	}
	return nil
}

// ValidateEnvName checks an environment variable name. It is written into the
// Caddyfile as {env.<name>}, so anything beyond a conventional variable name
// could change the meaning of the config.
func ValidateEnvName(name string) error {
	if name == "" {
		return fmt.Errorf("environment variable name cannot be empty")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !isAlphanumeric(c) && c != '_' {
			return fmt.Errorf("environment variable name %q may only contain letters, digits and underscores", name)
		}
	}
	if name[0] >= '0' && name[0] <= '9' {
		return fmt.Errorf("environment variable name %q must not start with a digit", name)
	}
	return nil
}

func isAlphanumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
