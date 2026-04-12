package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

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
