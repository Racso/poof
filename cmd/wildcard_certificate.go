package cmd

import (
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

var wildcardCertificateCmd = &cobra.Command{
	Use:   "wildcard-certificate",
	Short: "Serve a zone's subdomains under one wildcard certificate",
	Long: `Serve every direct subdomain of a zone under one *.<zone> certificate.

Every TLS certificate is published in public Certificate Transparency logs, so
a certificate per host makes the full list of a zone's hostnames public. Once a
zone is registered, every project and redirect exactly one label below it
(app.example.com for zone example.com) is routed under the zone's wildcard and
never gets a certificate naming it. The zone itself (example.com) and deeper
names (a.b.example.com) are not covered by the wildcard and keep their own.

A wildcard certificate requires the ACME DNS-01 challenge: Caddy must be built
with the DNS provider module, and the provider's API token must be set in the
Caddy container's environment, with access to the zone's DNS.`,
}

var wildcardCertificateAddCmd = &cobra.Command{
	Use:   "add <zone>",
	Short: "Register a zone (e.g. example.com) for a wildcard certificate",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		zone := args[0]
		dns, _ := cmd.Flags().GetString("dns")
		tokenEnv, _ := cmd.Flags().GetString("token-env")

		var result map[string]interface{}
		payload := map[string]string{"zone": zone, "dns_provider": dns, "token_env": tokenEnv}
		if err := apiPost("/wildcard-certificates", payload, &result); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ wildcard certificate added: *.%s (dns %s, token from %s)\n", result["zone"], dns, tokenEnv)
	},
}

var wildcardCertificateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List zones served by a wildcard certificate",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		var certs []map[string]interface{}
		if err := apiGet("/wildcard-certificates", &certs); err != nil {
			fatal("%v", err)
		}

		if len(certs) == 0 {
			fmt.Println("no wildcard certificates configured")
			return
		}

		fmt.Printf("%-30s %-14s %s\n", "ZONE", "DNS", "TOKEN ENV")
		fmt.Printf("%-30s %-14s %s\n", "----", "---", "---------")
		for _, c := range certs {
			zone, _ := c["zone"].(string)
			dns, _ := c["dns_provider"].(string)
			tokenEnv, _ := c["token_env"].(string)
			fmt.Printf("%-30s %-14s %s\n", zone, dns, tokenEnv)
		}
	},
}

var wildcardCertificateDeleteCmd = &cobra.Command{
	Use:   "delete <zone>",
	Short: "Stop serving a zone under a wildcard certificate",
	Long: `Stop serving a zone under a wildcard certificate.

Every site under the zone goes back to a certificate of its own, which
publishes each of their hostnames in Certificate Transparency logs. That
cannot be undone: a logged name stays public for good.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		zone := args[0]
		yes, _ := cmd.Flags().GetBool("yes")

		if !yes {
			fmt.Printf("Every site under %s will get its own certificate, publishing its hostname\n", zone)
			fmt.Println("in Certificate Transparency logs. Logged names stay public for good.")
			if !promptYN("Delete the wildcard certificate? [y/N] ", false) {
				fmt.Println("aborted")
				os.Exit(1)
			}
		}

		if err := apiDelete("/wildcard-certificates/" + url.PathEscape(zone)); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ wildcard certificate for %s deleted\n", zone)
	},
}

func init() {
	rootCmd.AddCommand(wildcardCertificateCmd)
	wildcardCertificateCmd.AddCommand(wildcardCertificateAddCmd)
	wildcardCertificateCmd.AddCommand(wildcardCertificateListCmd)
	wildcardCertificateCmd.AddCommand(wildcardCertificateDeleteCmd)

	wildcardCertificateAddCmd.Flags().String("dns", "cloudflare", "Caddy DNS provider module for the DNS-01 challenge")
	wildcardCertificateAddCmd.Flags().String("token-env", "CLOUDFLARE_API_TOKEN", "environment variable in the Caddy container holding the DNS provider's API token")
	wildcardCertificateDeleteCmd.Flags().BoolP("yes", "y", false, "skip confirmation prompt")
}
