package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func (s *Server) updateServer(w http.ResponseWriter, r *http.Request) {
	tag, err := fetchLatestTag()
	if err != nil {
		jsonError(w, fmt.Sprintf("could not fetch latest release: %v", err), http.StatusInternalServerError)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		jsonError(w, fmt.Sprintf("could not determine executable path: %v", err), http.StatusInternalServerError)
		return
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		jsonError(w, fmt.Sprintf("could not resolve executable path: %v", err), http.StatusInternalServerError)
		return
	}

	url := fmt.Sprintf("https://github.com/Racso/poof/releases/download/%s/poof-linux-amd64", tag)
	tmp, err := downloadBinary(filepath.Dir(exe), url)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := os.Chmod(tmp, 0755); err != nil {
		os.Remove(tmp)
		jsonError(w, fmt.Sprintf("could not set permissions: %v", err), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		jsonError(w, fmt.Sprintf("could not replace binary: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("update: binary replaced with %s, restarting via systemd", tag)
	jsonOK(w, map[string]string{"status": "restarting", "version": tag})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		if out, err := exec.Command("systemctl", "restart", "poof").CombinedOutput(); err != nil {
			log.Printf("update: systemctl restart failed: %v — %s", err, out)
		}
	}()
}

func fetchLatestTag() (string, error) {
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

func downloadBinary(dir, url string) (string, error) {
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
