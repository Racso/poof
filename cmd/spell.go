package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// Spells are curated recipes built on top of plain Poof primitives.
// The goal is to keep the flag surface of core commands small —
// prefer "poof spell X" over inventing a flag for every minor variation.

var spellCmd = &cobra.Command{
	Use:   "spell",
	Short: "Curated recipes built on top of plain Poof commands",
	Long: `Spells are named recipes that compose Poof primitives.

Each spell does one small composite thing that you'd otherwise do by
hand (set a Caddy snippet, flip a few flags, etc.). The point is to
keep the flag surface of core commands small — if a tweak can be
decomposed into a follow-up command, it lives here rather than as a
flag on 'poof add' or 'poof configure'.`,
}

// --- spell: proxy ---------------------------------------------------------

// Marker that identifies snippets the spell owns. If a snippet exists
// without this marker, the spell refuses to touch it — the operator
// must clear it (poof caddy delete) before re-casting.
const spellManagedMarker = "# [poof-spell] managed snippet — do not edit by hand"

// Per-entry marker line preceding each handle/reverse_proxy block.
// Format: "# [spell:proxy] path=<path> target=<upstream> strip_prefix=<bool>"
const spellProxyEntryPrefix = "# [spell:proxy]"

// Header prepended by the server when returning a snippet. The spell
// has to strip this before parsing.
const caddySnippetHashHeader = "# [poof-caddy] hash:sha256:"

type proxyEntry struct {
	Path        string // "" for whole-domain; otherwise "/api" form (leading slash, no trailing)
	Target      string // full Caddy upstream, e.g. "poof-dragonhub-engine:3000"
	StripPrefix bool   // only meaningful when Path != ""
}

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

The source project must already exist. The spell refuses to overwrite
a hand-written snippet — clear it first with 'poof caddy delete'.

Re-casting the same source/path updates the entry; casting different
paths on the same source accumulates. Whole-domain and path-specific
entries are mutually exclusive on a single source.`,
	Args: cobra.ExactArgs(2),
	Run:  runSpellProxy,
}

func runSpellProxy(cmd *cobra.Command, args []string) {
	sourceProject, sourcePath, err := parseSpellSource(args[0])
	if err != nil {
		fatal("%v", err)
	}

	// Verify source project exists. apiGet bubbles up the server's
	// "not found" message, which is plenty clear.
	var proj map[string]interface{}
	if err := apiGet("/projects/"+sourceProject, &proj); err != nil {
		fatal("source project: %v", err)
	}

	targetUpstream, err := resolveSpellTarget(args[1])
	if err != nil {
		fatal("%v", err)
	}

	// Fetch current snippet, strip the server's hash header, and check
	// it's either empty or spell-managed.
	var result map[string]interface{}
	if err := apiGet("/projects/"+sourceProject+"/caddy", &result); err != nil {
		fatal("%v", err)
	}
	current := stripHashHeader(asString(result["content"]))

	entries, managed := parseManagedSnippet(current)
	if !managed {
		fatal("project %q already has a hand-written Caddy snippet.\n  Clear it first: poof caddy delete %s",
			sourceProject, sourceProject)
	}

	newEntry := proxyEntry{
		Path:        sourcePath,
		Target:      targetUpstream,
		StripPrefix: sourcePath != "" && !spellProxyKeepPrefix,
	}

	updated, err := upsertProxyEntry(entries, newEntry)
	if err != nil {
		fatal("%v", err)
	}

	body := renderManagedSnippet(updated)
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
			sourceProject, sourcePath, targetUpstream, newEntry.StripPrefix)
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
		// "<project>/" treated as whole-domain.
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
		// container:port — pass through.
		return t, nil
	}
	// Bare name → must be a known project.
	var proj map[string]interface{}
	if err := apiGet("/projects/"+t, &proj); err != nil {
		return "", fmt.Errorf("target project %q: %v", t, err)
	}
	port, ok := proj["port"].(float64)
	if !ok || port <= 0 {
		return "", fmt.Errorf("target project %q has no port set (use <container>:<port> instead)", t)
	}
	return fmt.Sprintf("poof-%s:%d", t, int(port)), nil
}

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

// parseManagedSnippet returns the proxy entries encoded in a managed
// snippet body. The second return value is false when the body is
// non-empty and not spell-managed — the caller should refuse.
func parseManagedSnippet(raw string) ([]proxyEntry, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, true
	}
	if !strings.HasPrefix(trimmed, spellManagedMarker) {
		return nil, false
	}
	var entries []proxyEntry
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, spellProxyEntryPrefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, spellProxyEntryPrefix))
		e := proxyEntry{}
		for _, kv := range strings.Fields(rest) {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch k {
			case "path":
				e.Path = v
			case "target":
				e.Target = v
			case "strip_prefix":
				e.StripPrefix = v == "true"
			}
		}
		entries = append(entries, e)
	}
	return entries, true
}

// upsertProxyEntry inserts or replaces an entry keyed by path, enforcing
// the rule that whole-domain (path="") and path-scoped entries cannot
// coexist on a single source.
func upsertProxyEntry(entries []proxyEntry, newEntry proxyEntry) ([]proxyEntry, error) {
	for _, e := range entries {
		if e.Path == "" && newEntry.Path != "" {
			return nil, fmt.Errorf("source already has a whole-domain proxy (to %s); remove it first", e.Target)
		}
		if e.Path != "" && newEntry.Path == "" {
			return nil, fmt.Errorf("source already has path-scoped proxies (e.g. %s); a whole-domain proxy would conflict", e.Path)
		}
	}
	out := make([]proxyEntry, 0, len(entries)+1)
	replaced := false
	for _, e := range entries {
		if e.Path == newEntry.Path {
			out = append(out, newEntry)
			replaced = true
			continue
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, newEntry)
	}
	return out, nil
}

// renderManagedSnippet serializes entries back to a Caddy snippet body.
// Entries are sorted by path for deterministic output.
func renderManagedSnippet(entries []proxyEntry) string {
	if len(entries) == 0 {
		return ""
	}
	sorted := append([]proxyEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var b strings.Builder
	b.WriteString(spellManagedMarker)
	b.WriteString("\n")
	for i, e := range sorted {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s path=%s target=%s strip_prefix=%t\n",
			spellProxyEntryPrefix, e.Path, e.Target, e.StripPrefix)
		if e.Path == "" {
			fmt.Fprintf(&b, "reverse_proxy %s\n", e.Target)
		} else {
			fmt.Fprintf(&b, "handle %s/* {\n", e.Path)
			if e.StripPrefix {
				fmt.Fprintf(&b, "    uri strip_prefix %s\n", e.Path)
			}
			fmt.Fprintf(&b, "    reverse_proxy %s\n", e.Target)
			b.WriteString("}\n")
		}
	}
	return b.String()
}

func init() {
	rootCmd.AddCommand(spellCmd)
	spellCmd.AddCommand(spellProxyCmd)
	spellProxyCmd.Flags().BoolVar(&spellProxyKeepPrefix, "keep-prefix", false,
		"do not strip the source path before forwarding (off by default)")
}
