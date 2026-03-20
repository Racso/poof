package github

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/crypto/nacl/box"
)

const apiBase = "https://api.github.com"

// workflowTemplate is committed into each project repo at
// .github/workflows/poof.yml. POOF_BRANCH is replaced at commit time.
const workflowTemplate = `name: Poof! Deploy

on:
  push:
    branches: ["POOF_BRANCH"]

permissions:
  contents: read
  packages: write

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Build and push image
        run: |
          echo "${{ secrets.GITHUB_TOKEN }}" | docker login ghcr.io -u ${{ github.actor }} --password-stdin
          IMAGE=ghcr.io/$(echo "${{ github.repository }}" | tr '[:upper:]' '[:lower:]'):${{ github.sha }}
          docker build -t $IMAGE .
          docker push $IMAGE
          echo "IMAGE=$IMAGE" >> $GITHUB_ENV

      - name: Deploy via Poof!
        run: |
          curl -fsSL -X POST "${{ secrets.POOF_URL }}/projects/${{ github.event.repository.name }}/deploy" \
            -H "Authorization: Bearer ${{ secrets.POOF_TOKEN }}" \
            -H "Content-Type: application/json" \
            -d "{\"image\": \"${{ env.IMAGE }}\"}"
`

type Client struct {
	token string
}

func NewClient(token string) *Client {
	return &Client{token: token}
}

// SetupRepo sets the POOF_URL and POOF_TOKEN repo secrets and commits
// the deploy workflow file. This is called once by `poof add`.
func (c *Client) SetupRepo(owner, repo, poofURL, poofToken, branch string) error {
	if err := c.setSecret(owner, repo, "POOF_URL", poofURL); err != nil {
		return fmt.Errorf("set POOF_URL secret: %w", err)
	}
	if err := c.setSecret(owner, repo, "POOF_TOKEN", poofToken); err != nil {
		return fmt.Errorf("set POOF_TOKEN secret: %w", err)
	}
	if err := c.commitWorkflow(owner, repo, branch); err != nil {
		return fmt.Errorf("commit workflow: %w", err)
	}
	return nil
}

// RemoveRepo removes the POOF_TOKEN secret and deletes the workflow file.
func (c *Client) RemoveRepo(owner, repo string) error {
	_ = c.deleteSecret(owner, repo, "POOF_TOKEN")
	_ = c.deleteSecret(owner, repo, "POOF_URL")
	_ = c.deleteWorkflow(owner, repo)
	return nil
}

// --- Secrets ---

type repoPublicKey struct {
	KeyID string `json:"key_id"`
	Key   string `json:"key"`
}

func (c *Client) setSecret(owner, repo, name, value string) error {
	// Fetch the repo's NaCl public key.
	var pubKey repoPublicKey
	if err := c.get(fmt.Sprintf("/repos/%s/%s/actions/secrets/public-key", owner, repo), &pubKey); err != nil {
		return fmt.Errorf("get public key: %w", err)
	}

	encrypted, err := encryptSecret(pubKey.Key, value)
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}

	payload := map[string]string{
		"encrypted_value": encrypted,
		"key_id":          pubKey.KeyID,
	}
	return c.put(fmt.Sprintf("/repos/%s/%s/actions/secrets/%s", owner, repo, name), payload)
}

func (c *Client) deleteSecret(owner, repo, name string) error {
	return c.delete(fmt.Sprintf("/repos/%s/%s/actions/secrets/%s", owner, repo, name))
}

// encryptSecret encrypts a secret value using the repo's NaCl public key,
// as required by the GitHub Actions secrets API.
func encryptSecret(publicKeyB64, secret string) (string, error) {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return "", fmt.Errorf("decode public key: %w", err)
	}
	if len(pubKeyBytes) != 32 {
		return "", fmt.Errorf("unexpected public key length: %d", len(pubKeyBytes))
	}
	var recipientKey [32]byte
	copy(recipientKey[:], pubKeyBytes)

	encrypted, err := box.SealAnonymous(nil, []byte(secret), &recipientKey, rand.Reader)
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

// --- Workflow file ---

type fileContent struct {
	Message string `json:"message"`
	Content string `json:"content"` // base64
	SHA     string `json:"sha,omitempty"`
}

type getFileResponse struct {
	SHA string `json:"sha"`
}

func (c *Client) commitWorkflow(owner, repo, branch string) error {
	workflow := strings.ReplaceAll(workflowTemplate, "POOF_BRANCH", branch)
	encoded := base64.StdEncoding.EncodeToString([]byte(workflow))

	path := fmt.Sprintf("/repos/%s/%s/contents/.github/workflows/poof.yml", owner, repo)

	// Check if the file already exists (need SHA to update).
	var existing getFileResponse
	sha := ""
	if err := c.get(path, &existing); err == nil {
		sha = existing.SHA
	}

	payload := fileContent{
		Message: "chore: add Poof! deploy workflow",
		Content: encoded,
		SHA:     sha,
	}
	return c.put(path, payload)
}

func (c *Client) deleteWorkflow(owner, repo string) error {
	path := fmt.Sprintf("/repos/%s/%s/contents/.github/workflows/poof.yml", owner, repo)

	var existing getFileResponse
	if err := c.get(path, &existing); err != nil {
		return nil // file doesn't exist, nothing to delete
	}

	payload := map[string]string{
		"message": "chore: remove Poof! deploy workflow",
		"sha":     existing.SHA,
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("DELETE", apiBase+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API %s: %s", resp.Status, string(b))
	}
	return nil
}

// --- HTTP helpers ---

func (c *Client) get(path string, out interface{}) error {
	req, err := http.NewRequest("GET", apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API %s: %s", resp.Status, string(b))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) put(path string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PUT", apiBase+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API %s: %s", resp.Status, string(b))
	}
	return nil
}

func (c *Client) delete(path string) error {
	req, err := http.NewRequest("DELETE", apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API %s: %s", resp.Status, string(b))
	}
	return nil
}
