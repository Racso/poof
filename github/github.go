// Package github is the small GitHub REST client the Poof! CLI uses to put a
// workflow file into a repository, take it out again, and dispatch a run. It
// runs on the operator's machine with a credential that already lives there
// (gh, a token); the server never imports it.
package github

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const apiBase = "https://api.github.com"

// Client talks to the GitHub REST API with a bearer token.
type Client struct {
	token   string
	apiBase string
}

func NewClient(token string) *Client {
	return &Client{token: token, apiBase: apiBase}
}

// APIError is a non-2xx answer from GitHub, kept structured so callers can
// tell a missing scope (403) from a missing branch (404) and say so.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub API %d: %s", e.Status, strings.TrimSpace(e.Body))
}

// StatusOf returns the HTTP status behind err, or 0 when err is not an APIError.
func StatusOf(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

type fileResponse struct {
	SHA     string `json:"sha"`
	Content string `json:"content"`
}

// contentsPath is the Contents API path for a file.
func contentsPath(owner, repo, path string) string {
	return fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, path)
}

// refQuery returns a `?ref=<branch>` query string for Contents API GETs,
// or "" when branch is empty (GitHub then uses the default branch).
func refQuery(branch string) string {
	if branch == "" {
		return ""
	}
	return "?ref=" + url.QueryEscape(branch)
}

// PutFile creates or updates path on branch so it holds exactly content.
// It reports false when the file already matched and nothing was committed.
func (c *Client) PutFile(owner, repo, branch, path, content, message string) (changed bool, err error) {
	sha := ""
	var existing fileResponse
	found, err := c.getMaybe(contentsPath(owner, repo, path)+refQuery(branch), &existing)
	if err != nil {
		return false, err
	}
	if found {
		sha = existing.SHA
		current, decodeErr := base64.StdEncoding.DecodeString(strings.ReplaceAll(existing.Content, "\n", ""))
		if decodeErr == nil && string(current) == content {
			return false, nil
		}
	}
	payload := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
	}
	if sha != "" {
		payload["sha"] = sha
	}
	if branch != "" {
		payload["branch"] = branch
	}
	if err := c.send("PUT", contentsPath(owner, repo, path), payload, nil); err != nil {
		if StatusOf(err) == http.StatusNotFound && branch != "" && !c.branchExists(owner, repo, branch) {
			return false, fmt.Errorf("branch %q does not exist in %s/%s yet: push the branch first, then run 'poof refresh'", branch, owner, repo)
		}
		return false, err
	}
	return true, nil
}

// DeleteFile removes path from branch. A file that is not there is not an
// error: the goal is its absence.
func (c *Client) DeleteFile(owner, repo, branch, path, message string) (changed bool, err error) {
	var existing fileResponse
	found, err := c.getMaybe(contentsPath(owner, repo, path)+refQuery(branch), &existing)
	if err != nil {
		// A branch that no longer exists has no file to delete either.
		if StatusOf(err) == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	if !found {
		return false, nil
	}
	payload := map[string]string{"message": message, "sha": existing.SHA}
	if branch != "" {
		payload["branch"] = branch
	}
	if err := c.send("DELETE", contentsPath(owner, repo, path), payload, nil); err != nil {
		return false, err
	}
	return true, nil
}

// Dispatch fires workflow_dispatch for the workflow file (its basename, e.g.
// poof-auto-ci-web.yml) on ref, with inputs.
func (c *Client) Dispatch(owner, repo, workflowFile, ref string, inputs map[string]string) error {
	payload := map[string]interface{}{"ref": ref}
	if len(inputs) > 0 {
		payload["inputs"] = inputs
	}
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/dispatches", owner, repo, url.PathEscape(workflowFile))
	return c.send("POST", path, payload, nil)
}

// branchExists reports whether the branch exists in the repo. Errors are
// treated as "exists" so callers don't mask the original API failure with
// a wrong "branch not found" hint.
func (c *Client) branchExists(owner, repo, branch string) bool {
	path := fmt.Sprintf("/repos/%s/%s/branches/%s", owner, repo, url.PathEscape(branch))
	found, err := c.getMaybe(path, nil)
	if err != nil {
		return true
	}
	return found
}

// getMaybe GETs path and distinguishes 404 (found=false, nil error) from
// other failures.
func (c *Client) getMaybe(path string, out interface{}) (found bool, err error) {
	err = c.send("GET", path, nil, out)
	if StatusOf(err) == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}

func (c *Client) send(method, path string, payload, out interface{}) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.apiBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return &APIError{Status: resp.StatusCode, Body: string(b)}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
