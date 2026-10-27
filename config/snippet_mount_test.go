package config

import "testing"

func TestSnippetMountPoint(t *testing.T) {
	cases := []struct {
		staticDir string
		want      string
		ok        bool
	}{
		{"/etc/caddy/conf.d", "/var/lib/poof/caddy/conf.d", true},
		{"/etc/caddy/sites/enabled", "/var/lib/poof/caddy/sites/enabled", true},
		{"/etc/caddy", "", false},
		{"/srv/snippets", "", false},
		{"/etc/caddy-conf.d", "", false},
	}
	for _, c := range cases {
		got, ok := SnippetMountPoint("/var/lib/poof", c.staticDir)
		if got != c.want || ok != c.ok {
			t.Errorf("SnippetMountPoint(%q) = %q, %v; want %q, %v", c.staticDir, got, ok, c.want, c.ok)
		}
	}
}
