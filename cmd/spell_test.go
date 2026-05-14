package cmd

import (
	"strings"
	"testing"
)

func TestParseSpellSource(t *testing.T) {
	cases := []struct {
		in      string
		project string
		path    string
		wantErr bool
	}{
		{"dragonhub", "dragonhub", "", false},
		{"dragonhub/api", "dragonhub", "/api", false},
		{"dragonhub/api/v1", "dragonhub", "/api/v1", false},
		{"dragonhub/", "dragonhub", "", false},
		{"dragonhub/api/", "dragonhub", "/api", false},
		{"", "", "", true},
		{"/api", "", "", true},
	}
	for _, c := range cases {
		p, path, err := parseSpellSource(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSpellSource(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSpellSource(%q) unexpected error: %v", c.in, err)
			continue
		}
		if p != c.project || path != c.path {
			t.Errorf("parseSpellSource(%q) = (%q,%q), want (%q,%q)",
				c.in, p, path, c.project, c.path)
		}
	}
}

func TestStripHashHeader(t *testing.T) {
	raw := caddySnippetHashHeader + "deadbeef\nhello"
	if got, want := stripHashHeader(raw), "hello"; got != want {
		t.Errorf("stripHashHeader: got %q want %q", got, want)
	}
	if stripHashHeader("plain body") != "plain body" {
		t.Errorf("stripHashHeader should pass through non-headered content")
	}
}

func TestRenderProxySnippet_PathScoped(t *testing.T) {
	body := renderProxySnippet("dragonhub/api", "dragonhub-engine", "/api", "poof-dragonhub-engine:3000", true, false)
	mustContain(t, body, spellHeaderPrefix+"poof spell proxy dragonhub/api dragonhub-engine")
	mustContain(t, body, "handle /api/* {")
	mustContain(t, body, "uri strip_prefix /api")
	mustContain(t, body, "reverse_proxy poof-dragonhub-engine:3000")
}

func TestRenderProxySnippet_KeepPrefix(t *testing.T) {
	body := renderProxySnippet("dragonhub/api", "dragonhub-engine", "/api", "poof-dragonhub-engine:3000", false, true)
	if strings.Contains(body, "strip_prefix") {
		t.Errorf("rendered snippet should not strip prefix with --keep-prefix:\n%s", body)
	}
	mustContain(t, body, "--keep-prefix")
}

func TestRenderProxySnippet_WholeDomain(t *testing.T) {
	body := renderProxySnippet("indigo-ws", "indigo-app-racso:3000", "", "indigo-app-racso:3000", false, false)
	if strings.Contains(body, "handle") {
		t.Errorf("whole-domain entry should not use handle block:\n%s", body)
	}
	mustContain(t, body, "reverse_proxy indigo-app-racso:3000")
}

func TestRenderCleanURLsSnippet(t *testing.T) {
	body := renderCleanURLsSnippet("docs")
	mustContain(t, body, spellHeaderPrefix+"poof spell clean-urls docs")
	mustContain(t, body, "try_files {path}.html {path}")
}

func mustContain(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("rendered snippet missing %q\n%s", want, body)
	}
}
