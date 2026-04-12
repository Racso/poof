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

	"github.com/racso/poof/defaults"
	"golang.org/x/crypto/nacl/box"
)

const apiBase = "https://api.github.com"

// workflowTemplate is committed into each project repo at
// .github/workflows/poof-<name>.yml. Placeholders are replaced at commit time:
//   POOF_BRANCH        → branch to deploy on push
//   POOF_PATHS_BLOCK   → optional "    paths:\n      - ..." block (empty for root builds)
//   POOF_IMAGE_BASE    → Docker image base (without tag)
//   POOF_BUILD_ARGS    → docker build args ("." for root, "-f folder/Dockerfile folder" for subfolders)
//   POOF_PROJECT_NAME  → Poof! project name (may differ from GitHub repo name in monorepos)
//   POOF_PKG_NAME      → GHCR package name for cleanup
//   POOF_TOKEN_SECRET  → secret name: POOF_TOKEN for root builds, POOF_TOKEN_<NAME> for folder builds
const workflowTemplate = `name: POOF_WORKFLOW_NAME

on:
  push:
    branches: ["POOF_BRANCH"]
POOF_PATHS_BLOCK
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
          IMAGE=POOF_IMAGE_BASE:${{ github.sha }}
          docker build -t $IMAGE POOF_BUILD_ARGS
          docker push $IMAGE
          echo "IMAGE=$IMAGE" >> $GITHUB_ENV

      - name: Deploy via Poof!
        run: |
          curl -fsSL -X POST "${{ secrets.POOF_URL }}/projects/POOF_PROJECT_NAME/deploy" \
            -H "Authorization: Bearer ${{ secrets.POOF_TOKEN_SECRET }}" \
            -H "Content-Type: application/json" \
            -d "{\"image\": \"${{ env.IMAGE }}\"}"

      - name: Clean up old images
        uses: actions/delete-package-versions@v5
        with:
          package-name: POOF_PKG_NAME
          package-type: container
          min-versions-to-keep: 3
          delete-only-untagged-versions: false
`

type Client struct {
	token string
}

func NewClient(token string) *Client {
	return &Client{token: token}
}

// tokenSecretName returns the GitHub secret name used to store the per-project
// deploy token. Root builds use the plain "POOF_TOKEN" so existing repos are
// unaffected. Folder builds use "POOF_TOKEN_<NAME>" (uppercased, hyphens →
// underscores) so multiple projects in the same monorepo don't collide.
func tokenSecretName(projectName, folder string) string {
	if folder == "" {
		return "POOF_TOKEN"
	}
	var b strings.Builder
	b.WriteString("POOF_TOKEN_")
	for _, r := range strings.ToUpper(projectName) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// SetupRepo sets the POOF_URL and deploy-token repo secrets and commits
// the deploy workflow file. This is called once by `poof add` and on updates
// that change repo, branch, or folder.
//
// projectName is the Poof! project name (may differ from the GitHub repo name
// in monorepos). image is the custom image base (e.g. "ghcr.io/myorg/myimage");
// pass "" to derive it from the GitHub repository name. folder is the repo
// subfolder containing the Dockerfile (e.g. "frontend"); pass "" for a root build.
func (c *Client) SetupRepo(owner, repo, projectName, poofURL, poofToken, branch, image, folder string) error {
	if err := c.setSecret(owner, repo, "POOF_URL", poofURL); err != nil {
		return fmt.Errorf("set POOF_URL secret: %w", err)
	}
	secretName := tokenSecretName(projectName, folder)
	if err := c.setSecret(owner, repo, secretName, poofToken); err != nil {
		return fmt.Errorf("set %s secret: %w", secretName, err)
	}
	if err := c.commitWorkflow(owner, repo, projectName, branch, image, folder); err != nil {
		return fmt.Errorf("commit workflow: %w", err)
	}
	return nil
}

// RemoveRepo removes the deploy-token secret and deletes the workflow file.
func (c *Client) RemoveRepo(owner, repo, projectName, folder string) error {
	_ = c.deleteSecret(owner, repo, tokenSecretName(projectName, folder))
	_ = c.deleteSecret(owner, repo, "POOF_URL")
	_ = c.deleteWorkflow(owner, repo, projectName)
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

// imageBase strips any tag from image and returns the bare image reference.
func imageBase(image string) string {
	return strings.SplitN(image, ":", 2)[0]
}

// imagePackageName extracts the GHCR package name from an image reference.
// For "ghcr.io/owner/pkg" it returns "pkg"; for "ghcr.io/owner/sub/pkg" it
// returns "sub/pkg". Falls back to the last path component for other formats.
func imagePackageName(image string) string {
	base := imageBase(image)
	parts := strings.SplitN(base, "/", 3)
	if len(parts) == 3 {
		return parts[2]
	}
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		return base[idx+1:]
	}
	return base
}

func (c *Client) commitWorkflow(owner, repo, projectName, branch, image, folder string) error {
	// Default placeholders derive image and package name from the GitHub repo.
	imgBase := `ghcr.io/$(echo "${{ github.repository }}" | tr '[:upper:]' '[:lower:]')`
	pkgName := `${{ github.event.repository.name }}`
	if image != "" {
		imgBase = imageBase(image)
		pkgName = imagePackageName(image)
	}

	// folder support: path filter and build args
	pathsBlock := ""
	buildArgs := "."
	if folder != "" {
		folder = strings.TrimRight(folder, "/")
		pathsBlock = fmt.Sprintf("    paths:\n      - \"%s/**\"", folder)
		buildArgs = fmt.Sprintf("-f %s/Dockerfile %s", folder, folder)
	}

	// Build human-readable workflow name.
	workflowName := "Poof! deploy"
	var qualifiers []string
	if folder != "" {
		qualifiers = append(qualifiers, strings.TrimRight(folder, "/"))
	}
	if branch != defaults.Branch {
		qualifiers = append(qualifiers, branch)
	}
	if len(qualifiers) > 0 {
		workflowName += " (" + strings.Join(qualifiers, ", ") + ")"
	}

	workflow := strings.ReplaceAll(workflowTemplate, "POOF_WORKFLOW_NAME", workflowName)
	workflow = strings.ReplaceAll(workflow, "POOF_BRANCH", branch)
	workflow = strings.ReplaceAll(workflow, "POOF_PATHS_BLOCK", pathsBlock)
	workflow = strings.ReplaceAll(workflow, "POOF_IMAGE_BASE", imgBase)
	workflow = strings.ReplaceAll(workflow, "POOF_BUILD_ARGS", buildArgs)
	workflow = strings.ReplaceAll(workflow, "POOF_PROJECT_NAME", projectName)
	workflow = strings.ReplaceAll(workflow, "POOF_PKG_NAME", pkgName)
	workflow = strings.ReplaceAll(workflow, "POOF_TOKEN_SECRET", tokenSecretName(projectName, folder))
	encoded := base64.StdEncoding.EncodeToString([]byte(workflow))

	path := fmt.Sprintf("/repos/%s/%s/contents/.github/workflows/poof-%s.yml", owner, repo, projectName)

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

func (c *Client) deleteWorkflow(owner, repo, projectName string) error {
	path := fmt.Sprintf("/repos/%s/%s/contents/.github/workflows/poof-%s.yml", owner, repo, projectName)

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
