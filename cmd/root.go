package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/racso/poof/config"
	"github.com/racso/poof/version"
	"github.com/spf13/cobra"
)

var (
	cfg            *config.ClientConfig
	profileFlag    string
	profileEnvFlag bool
)

var rootCmd = &cobra.Command{
	Use:     "poof",
	Short:   "Poof! — lightweight self-hosted deployment daemon",
	Version: version.String(),
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
	// A routing failure does not stop the command it happened on — the stored
	// change is real — but it must not exit 0 either: scripts and CI would read
	// success from a host whose live routing is stale.
	if routingFailed {
		os.Exit(1)
	}
}

// routingErrorHeader is set by the server when a change was saved but Caddy
// refused the resulting config.
const routingErrorHeader = "X-Poof-Routing-Error"

var routingFailed bool

// reportRoutingError prints a Caddy sync failure where it cannot be missed.
// The command's own output stays on stdout and still reads as success, because
// the requested change did land in the database — what failed is publishing it.
func reportRoutingError(resp *http.Response) {
	msg := resp.Header.Get(routingErrorHeader)
	if msg == "" {
		return
	}
	routingFailed = true
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  ╭──────────────────────────────────────────────────────────────╮")
	fmt.Fprintln(os.Stderr, "  │  ROUTING NOT APPLIED — CADDY REJECTED THE NEW CONFIGURATION  │")
	fmt.Fprintln(os.Stderr, "  ╰──────────────────────────────────────────────────────────────╯")
	fmt.Fprintf(os.Stderr, "  %s\n", msg)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  Every project on this host keeps serving the last config Caddy")
	fmt.Fprintln(os.Stderr, "  accepted until this is fixed.")
	fmt.Fprintln(os.Stderr, "")
}

func init() {
	rootCmd.SetVersionTemplate("Poof! {{.Version}}\n")
	cobra.OnInitialize(loadConfig)
	rootCmd.PersistentFlags().StringVar(&profileFlag, "profile", "", "named profile to use from config")
	rootCmd.PersistentFlags().BoolVar(&profileEnvFlag, "profile-env", false, "read profile name from $POOF_PROFILE (errors if unset)")
	rootCmd.PersistentFlags().BoolVar(&passphraseStdin, "passphrase-stdin", false, "read the credentials passphrase from standard input instead of the terminal")
}

func loadConfig() {
	if profileFlag != "" && profileEnvFlag {
		fmt.Fprintln(os.Stderr, "error: --profile and --profile-env are mutually exclusive")
		os.Exit(1)
	}
	var err error
	cfg, err = config.LoadClient(profileFlag, profileEnvFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}
}

// activeProfile returns the profile name selected by --profile or
// --profile-env, or "" for the default (root-level) profile. Writers must use
// this so a `config set` under --profile never lands on the default profile.
func activeProfile() string {
	if profileEnvFlag {
		return os.Getenv("POOF_PROFILE")
	}
	return profileFlag
}

// profileSuffix renders the active profile for user-facing output.
func profileSuffix() string {
	if p := activeProfile(); p != "" {
		return fmt.Sprintf(" [%s]", p)
	}
	return ""
}

// troubleshootHint returns the `poof troubleshoot` invocation string,
// including the active profile flag if one is set.
func troubleshootHint() string {
	s := "poof troubleshoot"
	if profileFlag != "" {
		s += " --profile " + profileFlag
	} else if profileEnvFlag {
		s += " --profile-env"
	}
	return s
}

// serverURL returns the server API base URL. Fatals if server is not configured.
func serverURL() string {
	if cfg.Server == "" {
		fatal("server not configured — run: poof config set server <url>\n       (or: poof config set server   if this machine is the server)")
	}
	if cfg.Server == config.ServerLocal {
		return "http://localhost:9000"
	}
	return cfg.Server
}

// apiToken returns the API auth token. Fatals if token is not configured.
func apiToken() string {
	tok, _ := resolveToken()
	if tok == "" {
		fatal("token not configured — run: poof config set token <token>")
	}
	return tok
}

var resolvedToken struct {
	done   bool
	token  string
	source string
}

// resolveToken finds the API token for the active profile and says where it
// came from: "$POOF_TOKEN", "credentials" (the encrypted file), or
// "poof.toml" (the legacy clear-text field, still honoured). Empty when
// there is none. The credentials file is only opened when the environment
// has nothing, so commands that never reach the server never prompt.
func resolveToken() (token, source string) {
	if resolvedToken.done {
		return resolvedToken.token, resolvedToken.source
	}
	resolvedToken.done = true
	defer func() { resolvedToken.token, resolvedToken.source = token, source }()

	if v := os.Getenv("POOF_TOKEN"); v != "" {
		return v, "$POOF_TOKEN"
	}
	f := loadCredentials()
	if f.HasAPIToken(activeProfile()) {
		if tok := openCredentials(f).APITokens[activeProfile()]; tok != "" {
			return tok, "credentials"
		}
	}
	if cfg.Token != "" {
		if isatty.IsTerminal(os.Stderr.Fd()) {
			fmt.Fprintf(os.Stderr, "note: the API token is in %s in the clear; 'poof config set token <token>' moves it into the encrypted credentials file\n", config.ClientConfigPath())
		}
		return cfg.Token, "poof.toml"
	}
	return "", ""
}

// --- HTTP helpers for CLI commands ---

func apiGet(path string, out interface{}) error {
	req, err := http.NewRequest("GET", serverURL()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Poof! server at %s\nRun `%s` for help", serverURL(), troubleshootHint())
	}
	defer func() { _ = resp.Body.Close() }()

	reportRoutingError(resp)

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e map[string]string
		if json.Unmarshal(body, &e) == nil {
			return fmt.Errorf("server error: %s", e["error"])
		}
		return fmt.Errorf("server returned %s", resp.Status)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func apiPost(path string, payload interface{}, out interface{}) error {
	return apiRequest("POST", path, payload, out)
}

func apiPut(path string, payload interface{}, out interface{}) error {
	return apiRequest("PUT", path, payload, out)
}

func apiPatch(path string, payload interface{}, out interface{}) error {
	return apiRequest("PATCH", path, payload, out)
}

func apiDelete(path string) error {
	return apiRequest("DELETE", path, nil, nil)
}

// apiDeleteBody sends a DELETE with a JSON body — used where the thing being
// deleted is a set (e.g. detaching several network members at once).
func apiDeleteBody(path string, payload interface{}, out interface{}) error {
	return apiRequest("DELETE", path, payload, out)
}

func apiRequest(method, path string, payload interface{}, out interface{}) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, serverURL()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken())
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Poof! server at %s\nRun `%s` for help", serverURL(), troubleshootHint())
	}
	defer func() { _ = resp.Body.Close() }()

	reportRoutingError(resp)

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e map[string]string
		if json.Unmarshal(respBody, &e) == nil {
			return fmt.Errorf("server error: %s", e["error"])
		}
		return fmt.Errorf("server returned %s", resp.Status)
	}
	if out != nil {
		return json.Unmarshal(respBody, out)
	}
	return nil
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
