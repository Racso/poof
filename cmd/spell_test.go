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
	raw := caddySnippetHashHeader + "deadbeef\n" + spellManagedMarker + "\nhello"
	got := stripHashHeader(raw)
	want := spellManagedMarker + "\nhello"
	if got != want {
		t.Errorf("stripHashHeader: got %q want %q", got, want)
	}
	if stripHashHeader("plain body") != "plain body" {
		t.Errorf("stripHashHeader should pass through non-headered content")
	}
}

func TestParseManagedSnippet_EmptyIsManaged(t *testing.T) {
	entries, ok := parseManagedSnippet("")
	if !ok {
		t.Fatalf("empty snippet should be considered managed")
	}
	if len(entries) != 0 {
		t.Errorf("empty snippet should produce no entries, got %d", len(entries))
	}
}

func TestParseManagedSnippet_HandWrittenRejected(t *testing.T) {
	_, ok := parseManagedSnippet("reverse_proxy something:80")
	if ok {
		t.Fatalf("hand-written snippet should not be considered managed")
	}
}

func TestRoundTripSingleEntry(t *testing.T) {
	original := []proxyEntry{{Path: "/api", Target: "poof-engine:3000", StripPrefix: true}}
	body := renderManagedSnippet(original)
	if !strings.Contains(body, "uri strip_prefix /api") {
		t.Errorf("rendered snippet missing strip_prefix:\n%s", body)
	}
	if !strings.Contains(body, "reverse_proxy poof-engine:3000") {
		t.Errorf("rendered snippet missing reverse_proxy:\n%s", body)
	}
	parsed, ok := parseManagedSnippet(body)
	if !ok || len(parsed) != 1 {
		t.Fatalf("round-trip failed: ok=%v entries=%v", ok, parsed)
	}
	if parsed[0] != original[0] {
		t.Errorf("round-trip mismatch: got %+v want %+v", parsed[0], original[0])
	}
}

func TestRenderWholeDomain_NoStripPrefix(t *testing.T) {
	body := renderManagedSnippet([]proxyEntry{{Path: "", Target: "indigo:3000"}})
	if strings.Contains(body, "handle") {
		t.Errorf("whole-domain entry should not use handle block:\n%s", body)
	}
	if !strings.Contains(body, "reverse_proxy indigo:3000") {
		t.Errorf("whole-domain entry should have a bare reverse_proxy:\n%s", body)
	}
}

func TestUpsertReplacesSamePath(t *testing.T) {
	existing := []proxyEntry{{Path: "/api", Target: "old:80", StripPrefix: true}}
	updated, err := upsertProxyEntry(existing, proxyEntry{Path: "/api", Target: "new:80", StripPrefix: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updated) != 1 || updated[0].Target != "new:80" {
		t.Errorf("upsert should replace by path: %+v", updated)
	}
}

func TestUpsertAccumulatesDifferentPaths(t *testing.T) {
	existing := []proxyEntry{{Path: "/api", Target: "api:80", StripPrefix: true}}
	updated, err := upsertProxyEntry(existing, proxyEntry{Path: "/ws", Target: "ws:80", StripPrefix: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updated) != 2 {
		t.Errorf("upsert should accumulate distinct paths, got %d entries", len(updated))
	}
}

func TestUpsertRejectsWholeDomainOverPathScoped(t *testing.T) {
	existing := []proxyEntry{{Path: "/api", Target: "api:80", StripPrefix: true}}
	if _, err := upsertProxyEntry(existing, proxyEntry{Path: "", Target: "everything:80"}); err == nil {
		t.Errorf("should have refused whole-domain over path-scoped")
	}
}

func TestUpsertRejectsPathScopedOverWholeDomain(t *testing.T) {
	existing := []proxyEntry{{Path: "", Target: "everything:80"}}
	if _, err := upsertProxyEntry(existing, proxyEntry{Path: "/api", Target: "api:80", StripPrefix: true}); err == nil {
		t.Errorf("should have refused path-scoped over whole-domain")
	}
}
