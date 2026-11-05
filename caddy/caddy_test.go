package caddy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/racso/poof/store"
)

func generate(projects []store.Project, snippets map[string]string) string {
	return GenerateCaddyfile(projects, nil, nil, snippets, "", 9000, "/etc/caddy/conf.d")
}

func TestSPAFallbackLivesInCatchAllHandle(t *testing.T) {
	out := generate([]store.Project{
		{Name: "app", Domain: "app.rac.so", Static: "spa"},
	}, nil)

	if !strings.Contains(out, "\thandle {\n\t\ttry_files {path} /index.html\n\t\tfile_server\n\t}\n") {
		t.Errorf("expected SPA fallback inside a catch-all handle block, got:\n%s", out)
	}
	if strings.Contains(out, "\n\ttry_files") {
		t.Errorf("try_files must not appear at the top level (rewrite phase), got:\n%s", out)
	}
}

func TestSPASnippetHandleBlocksPrecedeFallback(t *testing.T) {
	snippet := "handle /api/* {\n\treverse_proxy backend:3000\n}"
	out := generate([]store.Project{
		{Name: "app", Domain: "app.rac.so", Static: "spa"},
	}, map[string]string{"app": snippet})

	apiIdx := strings.Index(out, "handle /api/*")
	fallbackIdx := strings.Index(out, "try_files {path} /index.html")
	if apiIdx == -1 || fallbackIdx == -1 {
		t.Fatalf("expected both snippet handle and SPA fallback, got:\n%s", out)
	}
	if apiIdx > fallbackIdx {
		t.Errorf("snippet handle blocks must come before the catch-all SPA fallback, got:\n%s", out)
	}
}

func TestPlainStaticKeepsTopLevelFileServer(t *testing.T) {
	out := generate([]store.Project{
		{Name: "site", Domain: "site.rac.so", Static: "static"},
	}, map[string]string{"site": "header X-Test on"})

	if !strings.Contains(out, "\tfile_server\n\theader X-Test on\n") {
		t.Errorf("expected top-level file_server followed by snippet, got:\n%s", out)
	}
}

func TestPausedProjectServes503AndHidesRouting(t *testing.T) {
	out := generate([]store.Project{
		{Name: "app", Domain: "app.rac.so", Port: 8080, Paused: true},
	}, map[string]string{"app": "handle /api/* {\n\treverse_proxy backend:3000\n}"})

	if !strings.Contains(out, "app.rac.so {\n\tlog\n\trespond \"This site is temporarily unavailable.\" 503\n}") {
		t.Errorf("expected a 503 block for the paused project, got:\n%s", out)
	}
	if strings.Contains(out, "reverse_proxy") {
		t.Errorf("paused project must not expose reverse_proxy or snippet routing, got:\n%s", out)
	}
}

func TestWriteFileReplacesTheCaddyfileAndLeavesNoTempFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caddy", "Caddyfile")
	if err := WriteFile(path, "first"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFile(path, "second"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil || string(written) != "second" {
		t.Fatalf("expected the second config, got %q (%v)", written, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("expected only the Caddyfile in its directory, found %d entries", len(entries))
	}
}

func TestStaticSitesHideDotfilesBeforeAnyOtherHandle(t *testing.T) {
	for _, mode := range []string{"static", "spa"} {
		out := generate([]store.Project{
			{Name: "site", Domain: "site.rac.so", Static: mode},
		}, map[string]string{"site": "handle /api/* {\n\treverse_proxy backend:3000\n}"})

		hide := strings.Index(out, "\t@poofDotfiles {\n\t\tpath_regexp /\\.\n\t\tnot path /.well-known/*\n\t}\n\thandle @poofDotfiles {\n\t\trespond 404\n\t}\n")
		if hide == -1 {
			t.Fatalf("%s: expected the dotfile 404 handle, got:\n%s", mode, out)
		}
		if api := strings.Index(out, "handle /api/*"); api < hide {
			t.Errorf("%s: dotfile handle must precede snippet handles, got:\n%s", mode, out)
		}
		if spa := strings.Index(out, "try_files {path} /index.html"); spa != -1 && spa < hide {
			t.Errorf("%s: dotfile handle must precede the SPA fallback, got:\n%s", mode, out)
		}
	}
}

func TestContainerProjectsDoNotHideDotfiles(t *testing.T) {
	out := generate([]store.Project{
		{Name: "app", Domain: "app.rac.so", Port: 8080},
	}, nil)
	if strings.Contains(out, "@poofDotfiles") {
		t.Errorf("a container project owns its own paths (e.g. /.well-known), got:\n%s", out)
	}
}

var racsoWildcard = []store.WildcardCertificate{{Zone: "rac.so", DNSProvider: "cloudflare", TokenEnv: "CLOUDFLARE_API_TOKEN"}}

func generateWithWildcards(projects []store.Project, redirects []store.Redirect, wildcards []store.WildcardCertificate, snippets map[string]string, poofHost string) string {
	return GenerateCaddyfile(projects, redirects, wildcards, snippets, poofHost, 9000, "/etc/caddy/conf.d")
}

func TestWildcardGroupsOnlyDirectSubdomains(t *testing.T) {
	out := generateWithWildcards([]store.Project{
		{Name: "app", Domain: "app.rac.so", Port: 80},
		{Name: "apex", Domain: "rac.so", Port: 80},
		{Name: "docs", Domain: "docs.by.rac.so", Port: 80},
		{Name: "client", Domain: "client.com", Port: 80},
	}, nil, racsoWildcard, nil, "")

	if strings.Contains(out, "\napp.rac.so {") {
		t.Errorf("a grouped domain must not get a site block of its own, got:\n%s", out)
	}
	if !strings.Contains(out, "\t@site_app host app.rac.so\n\thandle @site_app {\n\t\treverse_proxy poof-app:80\n\t}\n") {
		t.Errorf("expected app.rac.so routed inside the wildcard block, got:\n%s", out)
	}
	// The wildcard covers exactly one label: neither the zone itself nor a
	// deeper name is covered by the certificate, so neither may be grouped.
	for _, standalone := range []string{"\nrac.so {", "\ndocs.by.rac.so {", "\nclient.com {"} {
		if !strings.Contains(out, standalone) {
			t.Errorf("expected standalone block %q, got:\n%s", standalone, out)
		}
	}
}

func TestWildcardBlockObtainsTheWildcardOverDNS01(t *testing.T) {
	out := generateWithWildcards([]store.Project{{Name: "app", Domain: "app.rac.so", Port: 80}}, nil, racsoWildcard, nil, "")

	if !strings.Contains(out, "*.rac.so {\n\tlog\n\ttls {\n\t\tdns cloudflare {env.CLOUDFLARE_API_TOKEN}\n\t}\n") {
		t.Errorf("expected *.rac.so block with a DNS-01 tls directive, got:\n%s", out)
	}
}

func TestWildcardBlockEndsWithA404CatchAll(t *testing.T) {
	out := generateWithWildcards([]store.Project{{Name: "app", Domain: "app.rac.so", Port: 80}}, nil, racsoWildcard, nil, "")

	// An unregistered name under the zone resolves with valid TLS once the
	// wildcard exists; it must get a 404, not fall through to an empty 200.
	if !strings.HasSuffix(out, "\n\thandle {\n\t\trespond 404\n\t}\n}\n\n") {
		t.Errorf("expected the wildcard block to end with a 404 catch-all, got:\n%s", out)
	}
}

func TestWildcardBlockIsEmittedForAZoneWithNoSites(t *testing.T) {
	out := generateWithWildcards(nil, nil, racsoWildcard, nil, "")

	if !strings.Contains(out, "*.rac.so {\n") {
		t.Errorf("a registered zone must be emitted even with no sites, got:\n%s", out)
	}
}

func TestWildcardGroupsRedirectsAndThePoofHost(t *testing.T) {
	out := generateWithWildcards(nil,
		[]store.Redirect{{FromDomain: "old.rac.so", ToDomain: "new.example.com"}},
		racsoWildcard, nil, "p.rac.so")

	if !strings.Contains(out, "\t@site_old host old.rac.so\n\thandle @site_old {\n\t\tredir https://new.example.com{uri} 301\n\t}\n") {
		t.Errorf("expected the redirect routed inside the wildcard block, got:\n%s", out)
	}
	if !strings.Contains(out, "\t@site_p host p.rac.so\n\thandle @site_p {\n\t\treverse_proxy poof:9000\n\t}\n") {
		t.Errorf("expected the Poof API routed inside the wildcard block, got:\n%s", out)
	}
	if strings.Contains(out, "\np.rac.so {") || strings.Contains(out, "\nold.rac.so {") {
		t.Errorf("grouped domains must not get site blocks of their own, got:\n%s", out)
	}
}

func TestWildcardKeepsTheSPAFallbackInsideItsHost(t *testing.T) {
	out := generateWithWildcards([]store.Project{
		{Name: "app", Domain: "app.rac.so", Static: "spa"},
	}, nil, racsoWildcard, map[string]string{"app": "handle /api/* {\n\treverse_proxy backend:3000\n}"}, "")

	// The SPA fallback is a catch-all handle. At the wildcard block's top
	// level it would serve this site's index.html for every name in the zone;
	// nested in the site's host handle, it only catches this site's paths.
	if !strings.Contains(out, "\thandle @site_app {\n\t\troot * /var/lib/poof/static/app/current\n") {
		t.Fatalf("expected the static body nested in the host handle, got:\n%s", out)
	}
	if !strings.Contains(out, "\t\thandle {\n\t\t\ttry_files {path} /index.html\n\t\t\tfile_server\n\t\t}\n") {
		t.Errorf("expected the SPA fallback one level inside the host handle, got:\n%s", out)
	}
	if strings.Index(out, "handle /api/*") > strings.Index(out, "try_files {path} /index.html") {
		t.Errorf("snippet handles must still precede the SPA fallback, got:\n%s", out)
	}
}

func TestWildcardPausedSiteServes503InsideItsHost(t *testing.T) {
	out := generateWithWildcards([]store.Project{
		{Name: "app", Domain: "app.rac.so", Port: 80, Paused: true},
	}, nil, racsoWildcard, map[string]string{"app": "handle /api/* {\n\treverse_proxy backend:3000\n}"}, "")

	if !strings.Contains(out, "\thandle @site_app {\n\t\trespond \"This site is temporarily unavailable.\" 503\n\t}\n") {
		t.Errorf("expected a paused grouped site to answer 503, got:\n%s", out)
	}
	if strings.Contains(out, "backend:3000") {
		t.Errorf("a paused site's snippet must stay withheld, got:\n%s", out)
	}
}

func TestWildcardRoutesEachSiteUnderItsOwnZone(t *testing.T) {
	out := generateWithWildcards([]store.Project{
		{Name: "app", Domain: "app.rac.so", Port: 80},
		{Name: "docs", Domain: "docs.by.rac.so", Port: 80},
	}, nil, []store.WildcardCertificate{
		{Zone: "by.rac.so", DNSProvider: "cloudflare", TokenEnv: "CF"},
		{Zone: "rac.so", DNSProvider: "cloudflare", TokenEnv: "CF"},
	}, nil, "")

	byIdx := strings.Index(out, "*.by.rac.so {")
	racsoIdx := strings.Index(out, "*.rac.so {")
	docsIdx := strings.Index(out, "@site_docs host docs.by.rac.so")
	appIdx := strings.Index(out, "@site_app host app.rac.so")
	if byIdx == -1 || racsoIdx == -1 || docsIdx == -1 || appIdx == -1 {
		t.Fatalf("expected both zones and both sites, got:\n%s", out)
	}
	if !(byIdx < docsIdx && docsIdx < racsoIdx && racsoIdx < appIdx) {
		t.Errorf("each site must be routed in the block of the zone directly above it, got:\n%s", out)
	}
}

func TestWildcardMatchesDomainsCaseInsensitively(t *testing.T) {
	out := generateWithWildcards([]store.Project{{Name: "app", Domain: "App.Rac.So", Port: 80}}, nil, racsoWildcard, nil, "")

	if !strings.Contains(out, "\t@site_app host App.Rac.So\n") {
		t.Errorf("expected a mixed-case domain grouped under its zone, got:\n%s", out)
	}
}

func TestEverySiteBlockLogsAccess(t *testing.T) {
	out := GenerateCaddyfile([]store.Project{
		{Name: "app", Domain: "app.rac.so"},
		{Name: "apex", Domain: "rac.so"},
	}, nil, []store.WildcardCertificate{{Zone: "rac.so", DNSProvider: "cloudflare", TokenEnv: "T"}}, nil, "", 9000, "/etc/caddy/conf.d")

	if !strings.Contains(out, "rac.so {\n\tlog\n") {
		t.Errorf("standalone site block must log, got:\n%s", out)
	}
	if !strings.Contains(out, "*.rac.so {\n\tlog\n") {
		t.Errorf("wildcard block must log at its top level, so unregistered names are logged too, got:\n%s", out)
	}
	if !strings.Contains(out, "output file "+accessLogFile) || !strings.Contains(out, "include http.log.access") {
		t.Errorf("global options must route access logs to the file, got:\n%s", out)
	}
	if !strings.Contains(out, "log default {\n\t\texclude http.log.access\n\t}") {
		t.Errorf("default logger must exclude access logs, got:\n%s", out)
	}
}
