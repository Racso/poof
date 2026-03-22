package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/racso/poof/config"
	"github.com/spf13/cobra"
)

var (
	cfg            *config.ClientConfig
	profileFlag    string
	envProfileFlag bool
)

var rootCmd = &cobra.Command{
	Use:   "poof",
	Short: "Poof! — lightweight self-hosted deployment daemon",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(loadConfig)
	rootCmd.PersistentFlags().StringVar(&profileFlag, "profile", "", "named profile to use from config")
	rootCmd.PersistentFlags().BoolVar(&envProfileFlag, "env-profile", false, "read profile name from $POOF_PROFILE (errors if unset)")
}

func loadConfig() {
	if profileFlag != "" && envProfileFlag {
		fmt.Fprintln(os.Stderr, "error: --profile and --env-profile are mutually exclusive")
		os.Exit(1)
	}
	var err error
	cfg, err = config.LoadClient(profileFlag, envProfileFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}
}

// serverURL returns the server address from config or falls back to localhost.
func serverURL() string {
	if cfg.Server != "" {
		return cfg.Server
	}
	return "http://localhost:9000"
}

// apiToken returns the API token for CLI → server calls.
func apiToken() string {
	return cfg.Token
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
		return fmt.Errorf("could not reach poof server at %s: %w", serverURL(), err)
	}
	defer resp.Body.Close()

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
		return fmt.Errorf("could not reach poof server at %s: %w", serverURL(), err)
	}
	defer resp.Body.Close()

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
