package github

import (
	"strings"
	"testing"
)

func TestWorkflowPath(t *testing.T) {
	got := workflowPath("racso", "clip", "clip-api")
	want := "/repos/racso/clip/contents/.github/workflows/poof-auto-ci-clip-api.yml"
	if got != want {
		t.Errorf("workflowPath: got %q, want %q", got, want)
	}
}

func TestTriggerBlock_Managed(t *testing.T) {
	got := triggerBlock(CIModeManaged, "main", "")
	want := "on:\n  push:\n    branches: [\"main\"]\n"
	if got != want {
		t.Errorf("managed without folder:\n got: %q\nwant: %q", got, want)
	}
}

func TestTriggerBlock_ManagedWithFolder(t *testing.T) {
	got := triggerBlock(CIModeManaged, "main", "backend")
	want := "on:\n  push:\n    branches: [\"main\"]\n    paths:\n      - \"backend/**\"\n"
	if got != want {
		t.Errorf("managed with folder:\n got: %q\nwant: %q", got, want)
	}
}

func TestTriggerBlock_FolderTrailingSlashTrimmed(t *testing.T) {
	got := triggerBlock(CIModeManaged, "main", "backend/")
	if !strings.Contains(got, "\"backend/**\"") {
		t.Errorf("expected trailing-slash folder to be normalized to 'backend/**', got %q", got)
	}
}

func TestTriggerBlock_Callable(t *testing.T) {
	got := triggerBlock(CIModeCallable, "main", "backend")
	// Callable trigger ignores branch and folder — those are caller-decided.
	if strings.Contains(got, "push") {
		t.Errorf("callable trigger should not include push trigger, got: %q", got)
	}
	if strings.Contains(got, "main") || strings.Contains(got, "backend") {
		t.Errorf("callable trigger should not embed branch or folder, got: %q", got)
	}
	for _, want := range []string{
		"workflow_call:",
		"POOF_URL:",
		"POOF_TOKEN:",
		"required: true",
		"workflow_dispatch:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("callable trigger missing %q, got: %q", want, got)
		}
	}
}

func TestStaticDockerTemplate_CreateHasExplicitCommand(t *testing.T) {
	// `docker create` with no command fails on images without CMD/ENTRYPOINT
	// (e.g. a `FROM scratch` final stage), so the template must pass one.
	if !strings.Contains(staticWorkflowDockerTemplate, "docker create --name poof-extract poof-build noop-command") {
		t.Errorf("static docker workflow must pass an explicit command to docker create, got:\n%s", staticWorkflowDockerTemplate)
	}
}

func TestTriggerBlock_EmptyModeFallsBackToManaged(t *testing.T) {
	got := triggerBlock("", "main", "")
	if !strings.Contains(got, "push") || strings.Contains(got, "workflow_call") {
		t.Errorf("empty mode should default to managed (push), got: %q", got)
	}
}
