package caddy

import (
	"strings"
	"testing"

	"github.com/racso/poof/store"
)

func generate(projects []store.Project, snippets map[string]string) string {
	return GenerateCaddyfile(projects, nil, snippets, "rac.so", "", 9000, "/etc/caddy/conf.d")
}

func TestSPAFallbackLivesInCatchAllHandle(t *testing.T) {
	out := generate([]store.Project{
		{Name: "app", Domain: "app.rac.so", Static: "spa", Subpath: "disabled"},
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
		{Name: "app", Domain: "app.rac.so", Static: "spa", Subpath: "disabled"},
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
		{Name: "site", Domain: "site.rac.so", Static: "static", Subpath: "disabled"},
	}, map[string]string{"site": "header X-Test on"})

	if !strings.Contains(out, "\tfile_server\n\theader X-Test on\n") {
		t.Errorf("expected top-level file_server followed by snippet, got:\n%s", out)
	}
}

func TestPausedProjectServes503AndHidesRouting(t *testing.T) {
	out := generate([]store.Project{
		{Name: "app", Domain: "app.rac.so", Port: 8080, Subpath: "disabled", Paused: true},
	}, map[string]string{"app": "handle /api/* {\n\treverse_proxy backend:3000\n}"})

	if !strings.Contains(out, "app.rac.so {\n\trespond \"This site is temporarily unavailable.\" 503\n}") {
		t.Errorf("expected a 503 block for the paused project, got:\n%s", out)
	}
	if strings.Contains(out, "reverse_proxy") {
		t.Errorf("paused project must not expose reverse_proxy or snippet routing, got:\n%s", out)
	}
}

func TestPausedProjectSubpathAlso503s(t *testing.T) {
	out := generate([]store.Project{
		{Name: "app", Domain: "app.rac.so", Port: 8080, Subpath: "proxy", Paused: true},
	}, nil)

	if !strings.Contains(out, "\thandle_path /app/* {\n\t\trespond \"This site is temporarily unavailable.\" 503\n\t}") {
		t.Errorf("expected the subpath route to 503 while paused, got:\n%s", out)
	}
}
