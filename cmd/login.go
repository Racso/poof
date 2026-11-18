package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Give the server GitHub access through your account (device flow)",
	Long: `Give the server GitHub access through your account.

The server starts a GitHub device flow and prints a one-time code. Approve it
in your browser and the server receives a token that acts as you, limited to
what the Poof GitHub App may do (commit workflow files) on the repositories
you chose when installing the App. The token expires after eight hours and is
refreshed by the server for about six months; after that, run login again.

Install the App on your account first (the server prints the link). Revoke at
any time from GitHub, or forget the login on the server with 'poof logout'.`,
	Args: cobra.NoArgs,
	Run:  runLogin,
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Forget the GitHub login stored on the server",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		requireServerConnection()
		if err := apiDelete("/auth/github"); err != nil {
			fatal("%v", err)
		}
		fmt.Println("GitHub login cleared on the server.")
		fmt.Println("To revoke it on GitHub too: Settings → Applications → Authorized GitHub Apps → Poof.")
	},
}

func init() {
	loginCmd.Flags().Bool("status", false, "show the current GitHub credential and exit")
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
}

// authStatus is GET /auth/github.
type authStatus struct {
	Source           string    `json:"source"`
	Login            string    `json:"login"`
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	Token            string    `json:"token"`
	ClientID         string    `json:"client_id"`
	Pending          bool      `json:"pending"`
	PendingError     string    `json:"pending_error"`
	LoginError       string    `json:"login_error"`
}

func runLogin(cmd *cobra.Command, args []string) {
	requireServerConnection()
	if status, _ := cmd.Flags().GetBool("status"); status {
		var st authStatus
		if err := apiGet("/auth/github", &st); err != nil {
			fatal("%v", err)
		}
		printAuthStatus(st)
		return
	}

	var start struct {
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		AppURL          string `json:"app_url"`
	}
	if err := apiPost("/auth/github/device", nil, &start); err != nil {
		fatal("%v", err)
	}

	fmt.Printf("Open  %s\n", start.VerificationURI)
	fmt.Printf("Enter %s\n\n", start.UserCode)
	fmt.Printf("The Poof GitHub App must be installed on the account that owns your repositories:\n  %s\n\n", start.AppURL)
	fmt.Print("Waiting for approval")

	deadline := time.Now().Add(time.Duration(start.ExpiresIn) * time.Second)
	interval := time.Duration(start.Interval) * time.Second
	if interval < 2*time.Second {
		interval = 2 * time.Second
	}
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		var st authStatus
		if err := apiGet("/auth/github", &st); err != nil {
			fmt.Println()
			fatal("%v", err)
		}
		if st.Pending {
			fmt.Print(".")
			continue
		}
		fmt.Println()
		if st.PendingError != "" {
			fatal("GitHub login failed: %s", st.PendingError)
		}
		if st.Source != "login" {
			fatal("GitHub login did not complete")
		}
		fmt.Printf("Logged in as %s. Access token valid until %s; refreshed automatically until %s.\n",
			st.Login, st.ExpiresAt.Local().Format("2006-01-02 15:04"), st.RefreshExpiresAt.Local().Format("2006-01-02"))
		return
	}
	fmt.Println()
	fatal("the code expired before it was approved; run 'poof login' again")
}

func printAuthStatus(st authStatus) {
	switch st.Source {
	case "login":
		fmt.Printf("GitHub access: logged in as %s\n", st.Login)
		fmt.Printf("  access token valid until   %s\n", st.ExpiresAt.Local().Format("2006-01-02 15:04"))
		fmt.Printf("  refreshable until          %s\n", st.RefreshExpiresAt.Local().Format("2006-01-02"))
		if st.Token != "" {
			fmt.Printf("  also stored: github-token %s (unused while logged in; 'poof config unset github-token')\n", st.Token)
		}
	case "token":
		fmt.Printf("GitHub access: personal access token %s\n", st.Token)
	default:
		fmt.Println("GitHub access: none (run 'poof login')")
	}
	if st.LoginError != "" {
		fmt.Printf("  problem: %s\n", st.LoginError)
	}
	if st.Pending {
		fmt.Println("  a device-flow login is waiting for approval")
	} else if st.PendingError != "" {
		fmt.Printf("  last login attempt failed: %s\n", st.PendingError)
	}
}
