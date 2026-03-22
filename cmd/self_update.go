package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
)

var selfUpdateCmd = &cobra.Command{
	Use:   "update-self",
	Short: "Update the local poof binary to the latest release",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Checking latest release...")
		tag, err := latestReleaseTag()
		if err != nil {
			fatal("could not fetch latest release: %v", err)
		}
		fmt.Printf("Latest release: %s\n", tag)

		goos := runtime.GOOS
		goarch := runtime.GOARCH
		url := fmt.Sprintf(
			"https://github.com/Racso/poof/releases/download/%s/poof-%s-%s",
			tag, goos, goarch,
		)

		exe, err := os.Executable()
		if err != nil {
			fatal("could not determine executable path: %v", err)
		}
		exe, err = filepath.EvalSymlinks(exe)
		if err != nil {
			fatal("could not resolve executable path: %v", err)
		}

		fmt.Printf("Downloading %s/%s...\n", goos, goarch)
		tmp, err := downloadTo(filepath.Dir(exe), url)
		if err != nil {
			fatal("%v", err)
		}

		if err := os.Chmod(tmp, 0755); err != nil {
			os.Remove(tmp)
			fatal("could not set permissions: %v", err)
		}
		if err := os.Rename(tmp, exe); err != nil {
			os.Remove(tmp)
			if os.IsPermission(err) {
				fatal("permission denied writing to %s — try running with sudo", exe)
			}
			fatal("could not replace binary: %v", err)
		}

		fmt.Printf("Updated to %s\n", tag)
	},
}

func latestReleaseTag() (string, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/Racso/poof/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %s", resp.Status)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	if release.TagName == "" {
		return "", fmt.Errorf("no tag found in response")
	}
	return release.TagName, nil
}

func downloadTo(dir, url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: server returned %s", resp.Status)
	}

	f, err := os.CreateTemp(dir, "poof-*.new")
	if err != nil {
		if os.IsPermission(err) {
			return "", fmt.Errorf("permission denied writing to %s — try running with sudo", dir)
		}
		return "", fmt.Errorf("could not create temp file: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("download failed: %w", err)
	}
	f.Close()
	return f.Name(), nil
}

func init() {
	rootCmd.AddCommand(selfUpdateCmd)
}
