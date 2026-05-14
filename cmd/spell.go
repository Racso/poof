package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// Spells are curated recipes built on top of plain Poof primitives.
// The goal is to keep the flag surface of core commands small —
// prefer "poof spell X" over inventing a flag for every minor variation.
//
// Hardening policy (v1): a spell only writes a Caddy snippet when none
// exists. Re-casting requires the operator to clear the snippet first
// with `poof caddy delete`. This trades ergonomics (no in-place edits,
// no multi-path accumulation) for a strong guarantee: spells never
// silently overwrite hand edits or prior casts.

var spellCmd = &cobra.Command{
	Use:   "spell",
	Short: "Curated recipes built on top of plain Poof commands",
	Long: `Spells are named recipes that compose Poof primitives.

Each spell does one small composite thing that you'd otherwise do by
hand. If the project already has a Caddy snippet (from a previous cast
or hand-written), the spell refuses and tells you how to clear it.`,
}

// --- spell: proxy ---------------------------------------------------------

const spellHeaderPrefix = "# Cast by: "

var spellProxyKeepPrefix bool

var spellProxyCmd = &cobra.Command{
	Use:   "proxy <source> <target>",
	Short: "Route a domain (or path on it) through Caddy to another upstream",
	Long: `Install a Caddy reverse_proxy on <source>, sending traffic to <target>.

Source forms:
  <project>            whole domain of <project> proxies to <target>
  <project>/<path>     <path> on the project's domain proxies; rest falls through

Target forms:
  <project>            Poof project — container and port are looked up by name
  <container>:<port>   any container on poof-net (Poof- or hand-managed)

By default, the source path is stripped before the request is forwarded
(so the backend doesn't have to know it's mounted under /api). Pass
--keep-prefix to forward the path as-is.

Examples:
  poof spell proxy dragonhub/api dragonhub-engine
  poof spell proxy mysite indigo-app-racso:3000

The source project must already exist AND must not have a Caddy snippet.
If a snippet exists (from a previous cast or hand-written), the spell
refuses — clear it first with 'poof caddy delete <source>'.`,
	Args: cobra.ExactArgs(2),
	Run:  runSpellProxy,
}

func runSpellProxy(cmd *cobra.Command, args []string) {
	sourceProject, sourcePath, err := parseSpellSource(args[0])
	if err != nil {
		fatal("%v", err)
	}

	// Verify source project exists.
	var proj map[string]interface{}
	if err := apiGet("/projects/"+sourceProject, &proj); err != nil {
		fatal("source project: %v", err)
	}

	targetUpstream, err := resolveSpellTarget(args[1])
	if err != nil {
		fatal("%v", err)
	}

	// Refuse if any snippet exists. The server returns the body wrapped
	// in a hash header; stripping that leaves the raw stored content,
	// which is empty iff there's no snippet.
	var result map[string]interface{}
	if err := apiGet("/projects/"+sourceProject+"/caddy", &result); err != nil {
		fatal("%v", err)
	}
	if strings.TrimSpace(stripHashHeader(asString(result["content"]))) != "" {
		fatal("project %q already has a Caddy snippet.\n"+
			"  View it:   poof caddy get %s\n"+
			"  Clear it:  poof caddy delete %s",
			sourceProject, sourceProject, sourceProject)
	}

	stripPrefix := sourcePath != "" && !spellProxyKeepPrefix
	body := renderProxySnippet(args[0], args[1], sourcePath, targetUpstream, stripPrefix, spellProxyKeepPrefix)

	payload := map[string]interface{}{
		"content": body,
		"force":   true,
	}
	if err := apiPut("/projects/"+sourceProject+"/caddy", payload, nil); err != nil {
		fatal("%v", err)
	}

	if sourcePath == "" {
		fmt.Printf("✓ %s now proxies to %s\n", sourceProject, targetUpstream)
	} else {
		fmt.Printf("✓ %s%s now proxies to %s (strip_prefix=%t)\n",
			sourceProject, sourcePath, targetUpstream, stripPrefix)
	}
}

// parseSpellSource splits "<project>" or "<project>/<path>" into
// (project, normalizedPath). Normalized path has a leading slash and
// no trailing slash; "" means whole-domain.
func parseSpellSource(s string) (project, path string, err error) {
	if s == "" {
		return "", "", fmt.Errorf("source is empty")
	}
	project, rest, hasSlash := strings.Cut(s, "/")
	if project == "" {
		return "", "", fmt.Errorf("source %q has no project name", s)
	}
	if !hasSlash {
		return project, "", nil
	}
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		return project, "", nil
	}
	return project, "/" + rest, nil
}

// resolveSpellTarget converts "<project>" or "<container>:<port>" into
// a Caddy upstream string. A bare project name is resolved against the
// server (container = "poof-<name>", port from the project record).
func resolveSpellTarget(t string) (string, error) {
	if t == "" {
		return "", fmt.Errorf("target is empty")
	}
	if strings.Contains(t, ":") {
		return t, nil
	}
	var resp struct {
		Project struct {
			Port int `json:"port"`
		} `json:"project"`
	}
	if err := apiGet("/projects/"+t, &resp); err != nil {
		return "", fmt.Errorf("target project %q: %v", t, err)
	}
	if resp.Project.Port <= 0 {
		return "", fmt.Errorf("target project %q has no port set (use <container>:<port> instead)", t)
	}
	return fmt.Sprintf("poof-%s:%d", t, resp.Project.Port), nil
}

// renderProxySnippet emits a plain Caddy snippet for a single proxy entry.
// The leading comment records the cast command for humans only; nothing
// reads it back.
// --- spell: clean-urls ---------------------------------------------------

var spellCleanURLsCmd = &cobra.Command{
	Use:   "clean-urls <project>",
	Short: "Serve URLs without the .html extension (try_files fallback)",
	Long: `Install a try_files Caddy snippet that strips the .html extension
when serving static files. Equivalent snippet:

    try_files {path}.html {path}

So a request for /about returns /about.html if it exists, then /about
itself. Useful for static sites generated as flat directories of HTML
pages where you want bare-name URLs.

The source project must exist AND must not have a Caddy snippet.
If a snippet exists (from a previous cast or hand-written), the spell
refuses — clear it first with 'poof caddy delete <project>'.`,
	Args: cobra.ExactArgs(1),
	Run:  runSpellCleanURLs,
}

func runSpellCleanURLs(cmd *cobra.Command, args []string) {
	project := args[0]

	var proj map[string]interface{}
	if err := apiGet("/projects/"+project, &proj); err != nil {
		fatal("project: %v", err)
	}

	var result map[string]interface{}
	if err := apiGet("/projects/"+project+"/caddy", &result); err != nil {
		fatal("%v", err)
	}
	if strings.TrimSpace(stripHashHeader(asString(result["content"]))) != "" {
		fatal("project %q already has a Caddy snippet.\n"+
			"  View it:   poof caddy get %s\n"+
			"  Clear it:  poof caddy delete %s",
			project, project, project)
	}

	body := renderCleanURLsSnippet(project)
	payload := map[string]interface{}{"content": body, "force": true}
	if err := apiPut("/projects/"+project+"/caddy", payload, nil); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("✓ %s now serves clean URLs (try_files .html fallback)\n", project)
}

func renderCleanURLsSnippet(project string) string {
	return fmt.Sprintf("%spoof spell clean-urls %s\ntry_files {path}.html {path}\n",
		spellHeaderPrefix, project)
}

// --- spell: to-static ----------------------------------------------------

var (
	spellToStaticSPA   bool
	spellToStaticBuild bool
)

var spellToStaticCmd = &cobra.Command{
	Use:   "to-static <project>",
	Short: "Convert a container-served project to static (Caddy serves files directly)",
	Long: `Reconfigure <project> as a --static deploy. Equivalent to:

    poof configure <project> --static [--spa] [--build]

Refuses if the project is already static.

After the spell runs, you still need to deploy:

    poof deploy <project>   # from inside the repo to upload the files

or push a commit if CI is wired up. The old container keeps running
until the next deploy lands and is then stopped automatically.`,
	Args: cobra.ExactArgs(1),
	Run:  runSpellToStatic,
}

func runSpellToStatic(cmd *cobra.Command, args []string) {
	project := args[0]

	var resp struct {
		Project struct {
			Static string `json:"static"`
		} `json:"project"`
	}
	if err := apiGet("/projects/"+project, &resp); err != nil {
		fatal("project: %v", err)
	}
	current := resp.Project.Static
	if current == "static" || current == "spa" {
		fatal("project %q is already static (mode: %s); nothing to do",
			project, current)
	}

	mode := "static"
	if spellToStaticSPA {
		mode = "spa"
	}
	payload := map[string]interface{}{"static": mode}
	if spellToStaticBuild {
		payload["build"] = true
	}
	if err := apiPatch("/projects/"+project, payload, nil); err != nil {
		fatal("configure failed: %v", err)
	}

	fmt.Printf("✓ %s reconfigured (mode: %s, build: %t)\n", project, mode, spellToStaticBuild)
	fmt.Printf("  Next: deploy to actually swap the container out.\n")
	fmt.Printf("    poof deploy %s   (from inside the repo)\n", project)
	fmt.Printf("  Or push a commit if CI is wired up.\n")
}

// -------------------------------------------------------------------------

func renderProxySnippet(sourceArg, targetArg, path, upstream string, stripPrefix, keepPrefix bool) string {
	cmd := fmt.Sprintf("poof spell proxy %s %s", sourceArg, targetArg)
	if keepPrefix {
		cmd += " --keep-prefix"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s%s\n", spellHeaderPrefix, cmd)
	if path == "" {
		fmt.Fprintf(&b, "reverse_proxy %s\n", upstream)
	} else {
		fmt.Fprintf(&b, "handle %s/* {\n", path)
		if stripPrefix {
			fmt.Fprintf(&b, "    uri strip_prefix %s\n", path)
		}
		fmt.Fprintf(&b, "    reverse_proxy %s\n", upstream)
		b.WriteString("}\n")
	}
	return b.String()
}

const caddySnippetHashHeader = "# [poof-caddy] hash:sha256:"

func stripHashHeader(s string) string {
	if !strings.HasPrefix(s, caddySnippetHashHeader) {
		return s
	}
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return ""
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

func init() {
	rootCmd.AddCommand(spellCmd)

	spellCmd.AddCommand(spellProxyCmd)
	spellProxyCmd.Flags().BoolVar(&spellProxyKeepPrefix, "keep-prefix", false,
		"do not strip the source path before forwarding (off by default)")

	spellCmd.AddCommand(spellCleanURLsCmd)

	spellCmd.AddCommand(spellToStaticCmd)
	spellToStaticCmd.Flags().BoolVar(&spellToStaticSPA, "spa", false,
		"convert to SPA mode (try_files fallback to /index.html)")
	spellToStaticCmd.Flags().BoolVar(&spellToStaticBuild, "build", false,
		"use the repo's Dockerfile to build the static files")
}
