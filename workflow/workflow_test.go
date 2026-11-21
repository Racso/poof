package workflow

import (
	"strings"
	"testing"
)

func TestFile(t *testing.T) {
	got := File("clip-api")
	want := ".github/workflows/poof-auto-ci-clip-api.yml"
	if got != want {
		t.Errorf("File: got %q, want %q", got, want)
	}
}

func TestTriggerBlock_Managed(t *testing.T) {
	got := triggerBlock(CIModeManaged, "main", "", false)
	want := "on:\n  push:\n    branches: [\"main\"]\n  workflow_dispatch:\n"
	if got != want {
		t.Errorf("managed without folder:\n got: %q\nwant: %q", got, want)
	}
}

func TestTriggerBlock_ManagedWithFolder(t *testing.T) {
	got := triggerBlock(CIModeManaged, "main", "backend", false)
	want := "on:\n  push:\n    branches: [\"main\"]\n    paths:\n      - \"backend/**\"\n  workflow_dispatch:\n"
	if got != want {
		t.Errorf("managed with folder:\n got: %q\nwant: %q", got, want)
	}
}

func TestTriggerBlock_ManagedWithTagInput(t *testing.T) {
	got := triggerBlock(CIModeManaged, "main", "", true)
	for _, want := range []string{"workflow_dispatch:", "inputs:", "tag:", "required: false"} {
		if !strings.Contains(got, want) {
			t.Errorf("managed trigger with tag input missing %q, got: %q", want, got)
		}
	}
}

func TestTriggerBlock_FolderTrailingSlashTrimmed(t *testing.T) {
	got := triggerBlock(CIModeManaged, "main", "backend/", false)
	if !strings.Contains(got, "\"backend/**\"") {
		t.Errorf("expected trailing-slash folder to be normalized to 'backend/**', got %q", got)
	}
}

func TestTriggerBlock_Callable(t *testing.T) {
	got := triggerBlock(CIModeCallable, "main", "backend", true)
	// Callable trigger ignores branch and folder — those are caller-decided.
	if strings.Contains(got, "push") {
		t.Errorf("callable trigger should not include push trigger, got: %q", got)
	}
	if strings.Contains(got, "main") || strings.Contains(got, "backend") {
		t.Errorf("callable trigger should not embed branch or folder, got: %q", got)
	}
	for _, want := range []string{"workflow_call:", "workflow_dispatch:"} {
		if !strings.Contains(got, want) {
			t.Errorf("callable trigger missing %q, got: %q", want, got)
		}
	}
	if strings.Contains(got, "POOF_TOKEN") || strings.Contains(got, "secrets:") {
		t.Errorf("callable trigger must not declare the retired secrets, got: %q", got)
	}
}

func TestStaticDockerTemplate_CreateHasExplicitCommand(t *testing.T) {
	// `docker create` with no command fails on images without CMD/ENTRYPOINT
	// (e.g. a `FROM scratch` final stage), so the template must pass one.
	if !strings.Contains(staticDockerTemplate, "docker create --name poof-extract poof-build noop-command") {
		t.Errorf("static docker workflow must pass an explicit command to docker create, got:\n%s", staticDockerTemplate)
	}
}

func TestTriggerBlock_EmptyModeFallsBackToManaged(t *testing.T) {
	got := triggerBlock("", "main", "", false)
	if !strings.Contains(got, "push") || strings.Contains(got, "workflow_call") {
		t.Errorf("empty mode should default to managed (push), got: %q", got)
	}
}

func TestTemplates_AuthenticateWithOIDCAndJobToken(t *testing.T) {
	for name, tpl := range map[string]string{
		"container":     containerTemplate,
		"static docker": staticDockerTemplate,
		"static direct": staticDirectTemplate,
	} {
		for _, want := range []string{"id-token: write", "ACTIONS_ID_TOKEN_REQUEST_URL", "audience=$POOF_URL", `POOF_URL="POOF_SERVER_URL"`} {
			if !strings.Contains(tpl, want) {
				t.Errorf("%s template missing %q", name, want)
			}
		}
		for _, stale := range []string{"secrets.POOF_TOKEN", "secrets.POOF_URL"} {
			if strings.Contains(tpl, stale) {
				t.Errorf("%s template still reads %s", name, stale)
			}
		}
	}
	// Only the container workflow has an image for the server to pull.
	if !strings.Contains(containerTemplate, "X-Poof-Registry-Token: ${{ secrets.GITHUB_TOKEN }}") {
		t.Error("container template must hand the job token to the server for the pull")
	}
}

func TestRender_ContainerSubstitutesEverything(t *testing.T) {
	got := Render(Spec{
		ProjectName: "web", PoofURL: "https://poof.rac.so/", Branch: "main",
		Image: "ghcr.io/racso/web:ignored", CIMode: CIModeManaged,
	})
	for _, want := range []string{
		"name: Poof! deploy\n",
		`branches: ["main"]`,
		"IMAGE=ghcr.io/racso/web:${{ github.sha }}",
		"package-name: web",
		`POOF_URL="https://poof.rac.so"`,
		"/projects/web/deploy",
		"inputs.tag",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered container workflow missing %q", want)
		}
	}
	if strings.Contains(got, "POOF_") && !strings.Contains(got, "POOF_URL=") {
		t.Errorf("unreplaced placeholder in:\n%s", got)
	}
	for _, placeholder := range []string{"POOF_TRIGGER_BLOCK", "POOF_IMAGE_BASE", "POOF_BUILD_ARGS", "POOF_PROJECT_NAME", "POOF_PKG_NAME", "POOF_SERVER_URL", "POOF_WORKFLOW_NAME"} {
		if strings.Contains(got, placeholder) {
			t.Errorf("placeholder %s left in rendered workflow", placeholder)
		}
	}
}

func TestRender_StaticHasNoTagInput(t *testing.T) {
	got := Render(Spec{ProjectName: "site", PoofURL: "https://poof.rac.so", Branch: "main", Static: "spa", Folder: "www/"})
	if strings.Contains(got, "inputs:") {
		t.Error("static workflow has no registry image to redeploy; it must not offer a tag input")
	}
	if !strings.Contains(got, "-C www .") {
		t.Errorf("static direct workflow should upload the folder, got:\n%s", got)
	}
	if !strings.Contains(got, "name: Poof! deploy (static, www)") {
		t.Errorf("workflow name should carry its qualifiers, got:\n%s", got)
	}
}

func TestRender_IsDeterministic(t *testing.T) {
	spec := Spec{ProjectName: "web", PoofURL: "https://poof.rac.so", Branch: "staging", Image: "ghcr.io/racso/web"}
	if Render(spec) != Render(spec) {
		t.Error("Render must be deterministic")
	}
}
