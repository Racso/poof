package github

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub answers 404 for every path not in found, and records writes.
type fakeGitHub struct {
	found  map[string]string // path -> JSON body for GET
	writes []string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
		w.Write([]byte(`{}`))
		return
	}
	if body, ok := f.found[r.URL.Path]; ok {
		w.Write([]byte(body))
		return
	}
	http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
}

func client(t *testing.T, f *fakeGitHub) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return NewClientAt("tok", srv.URL)
}

const wf = ".github/workflows/poof-auto-ci-web.yml"

func TestDeleteFileInInvisibleRepoIsAnError(t *testing.T) {
	f := &fakeGitHub{found: map[string]string{}}
	changed, err := client(t, f).DeleteFile("racso", "secret", "main", wf, "m")
	if !errors.Is(err, ErrRepoNotVisible) || changed {
		t.Fatalf("expected ErrRepoNotVisible, got changed=%v err=%v", changed, err)
	}
	if len(f.writes) != 0 {
		t.Errorf("nothing should be written: %v", f.writes)
	}
}

func TestDeleteFileMissingInVisibleRepoIsNotAnError(t *testing.T) {
	f := &fakeGitHub{found: map[string]string{"/repos/racso/web": `{}`}}
	changed, err := client(t, f).DeleteFile("racso", "web", "main", wf, "m")
	if err != nil || changed {
		t.Fatalf("expected nothing to remove, got changed=%v err=%v", changed, err)
	}
}

func TestDeleteFilePresentIsDeleted(t *testing.T) {
	f := &fakeGitHub{found: map[string]string{
		"/repos/racso/web/contents/" + wf: `{"sha":"abc"}`,
	}}
	changed, err := client(t, f).DeleteFile("racso", "web", "main", wf, "m")
	if err != nil || !changed || len(f.writes) != 1 || !strings.HasPrefix(f.writes[0], "DELETE") {
		t.Fatalf("expected one DELETE, got changed=%v err=%v writes=%v", changed, err, f.writes)
	}
}

func TestPutFileInInvisibleRepoIsAnError(t *testing.T) {
	f := &fakeGitHub{found: map[string]string{}}
	_, err := client(t, f).PutFile("racso", "secret", "main", wf, "x", "m")
	if !errors.Is(err, ErrRepoNotVisible) {
		t.Fatalf("expected ErrRepoNotVisible, got %v", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("nothing should be written: %v", f.writes)
	}
}

func TestPutFileNewInVisibleRepoIsWritten(t *testing.T) {
	f := &fakeGitHub{found: map[string]string{"/repos/racso/web": `{}`}}
	changed, err := client(t, f).PutFile("racso", "web", "main", wf, "x", "m")
	if err != nil || !changed || len(f.writes) != 1 || !strings.HasPrefix(f.writes[0], "PUT") {
		t.Fatalf("expected one PUT, got changed=%v err=%v writes=%v", changed, err, f.writes)
	}
}
