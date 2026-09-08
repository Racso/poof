package cmd

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/racso/poof/config"
	"github.com/spf13/cobra"
)

var troubleshootCmd = &cobra.Command{
	Use:   "troubleshoot",
	Short: "Diagnose why the Poof! server is unreachable",
	Long: `Probe the configured server step by step — config, DNS, TCP, TLS,
HTTP, auth — and report the first thing that actually fails.

Runs entirely from the client. It never needs a working connection, which
is the point: it is what you run when nothing else works.`,
	Args: cobra.NoArgs,
	Run:  runTroubleshoot,
}

func runTroubleshoot(cmd *cobra.Command, args []string) {
	ok := func(format string, a ...interface{}) {
		fmt.Printf("  ✓ "+format+"\n", a...)
	}
	bad := func(format string, a ...interface{}) {
		fmt.Printf("  ✗ "+format+"\n", a...)
	}
	next := func(format string, a ...interface{}) {
		fmt.Printf("\nNext step:\n  "+format+"\n", a...)
	}

	// --- 1. Config ---
	fmt.Println("config")
	ok("file: %s", config.ClientConfigPath())
	switch {
	case profileFlag != "":
		ok("profile: %s (--profile)", profileFlag)
	case profileEnvFlag:
		ok("profile: %s ($POOF_PROFILE)", os.Getenv("POOF_PROFILE"))
	default:
		ok("profile: (default)")
	}
	if v := os.Getenv("POOF_SERVER"); v != "" {
		ok("$POOF_SERVER is set and overrides the config file")
	}
	if os.Getenv("POOF_TOKEN") != "" {
		ok("$POOF_TOKEN is set and overrides the config file")
	}

	if cfg.Server == "" {
		bad("server: not set")
		next("poof config set server <url>\n  (or `poof config set server` if this machine is the server)")
		return
	}
	ok("server: %s", cfg.Server)
	if cfg.Token == "" {
		bad("token: not set")
		next("poof config set token <token>")
		return
	}
	ok("token: set (%d chars)", len(cfg.Token))

	base := serverURL()
	u, err := url.Parse(base)
	if err != nil {
		bad("server URL does not parse: %v", err)
		next("poof config set server <url>   — expected form: https://poof.example.com")
		return
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	// --- 2. DNS ---
	fmt.Println("\ndns")
	if net.ParseIP(host) != nil {
		ok("%s is a literal IP, no lookup needed", host)
	} else {
		addrs, err := net.LookupHost(host)
		if err != nil {
			bad("cannot resolve %s: %v", host, err)
			next("check the hostname, or point a DNS record at the server's IP")
			return
		}
		ok("%s → %s", host, strings.Join(addrs, ", "))
	}

	// --- 3. TCP ---
	fmt.Println("\ntcp")
	addr := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		bad("cannot connect to %s: %v", addr, err)
		if strings.Contains(err.Error(), "refused") {
			next("nothing is listening on %s. On the server:\n    docker ps | grep -E 'caddy|poof'\n    docker logs poof --tail 50", addr)
		} else {
			next("connection to %s timed out — likely a firewall between you and the host", addr)
		}
		return
	}
	conn.Close()
	ok("connected to %s", addr)

	// --- 4. TLS ---
	if u.Scheme == "https" {
		fmt.Println("\ntls")
		tconn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, nil)
		if err != nil {
			bad("handshake failed: %v", err)
			next("Caddy may not have a certificate for %s yet.\n  Check that DNS is NOT proxied through Cloudflare (ACME needs direct reach), then:\n    docker logs caddy-proxy --tail 50", host)
			return
		}
		cert := tconn.ConnectionState().PeerCertificates[0]
		tconn.Close()
		ok("certificate valid until %s", cert.NotAfter.Format("2006-01-02"))
	}

	// --- 5. HTTP + auth ---
	fmt.Println("\napi")
	client := &http.Client{Timeout: 10 * time.Second}

	req, _ := http.NewRequest("GET", base+"/version", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := client.Do(req)
	if err != nil {
		bad("GET /version failed: %v", err)
		next("the port is open but the API did not answer — is Caddy routing %s to the poof container?\n    docker logs caddy-proxy --tail 50", host)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		bad("GET /version → %s — the server rejected this token", resp.Status)
		next("the token in %s does not match the server's.\n  On the server: grep token /etc/poof/poof.toml\n  Then: poof config set token <token>", config.ClientConfigPath())
	case resp.StatusCode == http.StatusNotFound:
		bad("GET /version → 404 — something answered, but it is not Poof!")
		next("%s is routed to the wrong upstream. Check the Caddy site block for this domain.", host)
	case resp.StatusCode >= 500:
		bad("GET /version → %s — the server is up but erroring", resp.Status)
		next("poof server-logs   (or, on the host: docker logs poof --tail 50)")
	case resp.StatusCode != http.StatusOK:
		bad("GET /version → %s", resp.Status)
		next("unexpected status; check the server logs: docker logs poof --tail 50")
	default:
		var v struct {
			Number string `json:"number"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&v)
		ok("GET /version → 200 (server %s)", firstNonEmpty(v.Number, "unknown"))
		fmt.Printf("\nThe server is reachable and this token works.\n")
		fmt.Printf("If a specific command still fails, the problem is that command, not connectivity.\n")
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func init() {
	rootCmd.AddCommand(troubleshootCmd)
}
