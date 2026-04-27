package docker

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// LocalImage represents one tagged image found locally.
type LocalImage struct {
	Reference string    // e.g. "ghcr.io/foo/bar:abc123"
	ID        string    // image ID (sha256:...)
	Created   time.Time
}

// GCResult summarizes what GC did for a project.
type GCResult struct {
	Project string   `json:"project"`
	Removed []string `json:"removed,omitempty"` // references that were (or would be) deleted
	Kept    []string `json:"kept,omitempty"`    // references that survived
	Failed  []string `json:"failed,omitempty"`  // references docker refused to delete
}

// imageRepo returns the repo portion of an image reference (everything before the
// final tag). "ghcr.io/foo/bar:abc" → "ghcr.io/foo/bar".
func imageRepo(ref string) string {
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon]
	}
	return ref
}

// listImagesForRepo returns all locally cached tags for the repo of the given image.
func listImagesForRepo(image string) ([]LocalImage, error) {
	repo := imageRepo(image)
	out, err := exec.Command(
		"docker", "images", "--no-trunc",
		"--format", "{{.Repository}}:{{.Tag}}|{{.ID}}|{{.CreatedAt}}",
		repo,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("docker images: %w", err)
	}

	var images []LocalImage
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		ref, id, createdAt := parts[0], parts[1], parts[2]
		// Skip dangling tags (Repository or Tag is "<none>").
		if strings.Contains(ref, "<none>") {
			continue
		}
		t, err := parseDockerTime(createdAt)
		if err != nil {
			continue
		}
		images = append(images, LocalImage{Reference: ref, ID: id, Created: t})
	}
	return images, nil
}

// parseDockerTime parses the format Docker uses for CreatedAt:
// "2024-01-15 10:30:00 +0000 UTC".
func parseDockerTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	layouts := []string{
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05 -0700",
		time.RFC3339,
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q", s)
}

// runningImageID returns the image ID of the project's container, or "" if
// the container does not exist.
func runningImageID(projectName string) string {
	out, err := exec.Command(
		"docker", "inspect", "-f", "{{.Image}}", containerFor(projectName),
	).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// GC removes old images for a project. Behavior:
//   - Images are sorted newest first.
//   - The image of the project's running container is never deleted.
//   - When both keep and olderThanDays are >0, an image must satisfy BOTH
//     conditions to be deleted (outside keep window AND older than N days).
//   - When only one is set, that condition alone governs deletion.
//   - When both are 0, nothing is deleted.
//   - dryRun=true reports what would be removed without calling docker rmi.
func GC(projectName, image string, keep, olderThanDays int, dryRun bool) (GCResult, error) {
	res := GCResult{Project: projectName}
	if keep <= 0 && olderThanDays <= 0 {
		return res, nil
	}

	images, err := listImagesForRepo(image)
	if err != nil {
		return res, err
	}
	sort.Slice(images, func(i, j int) bool {
		return images[i].Created.After(images[j].Created)
	})

	runningID := runningImageID(projectName)

	var cutoff time.Time
	if olderThanDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -olderThanDays)
	}

	for i, img := range images {
		if runningID != "" && img.ID == runningID {
			res.Kept = append(res.Kept, img.Reference)
			continue
		}

		outsideKeep := keep > 0 && i >= keep
		olderThan := !cutoff.IsZero() && img.Created.Before(cutoff)

		var eligible bool
		switch {
		case keep > 0 && olderThanDays > 0:
			eligible = outsideKeep && olderThan
		case keep > 0:
			eligible = outsideKeep
		case olderThanDays > 0:
			eligible = olderThan
		}

		if !eligible {
			res.Kept = append(res.Kept, img.Reference)
			continue
		}

		if dryRun {
			res.Removed = append(res.Removed, img.Reference)
			continue
		}

		if err := removeImage(img.Reference); err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", img.Reference, err))
			continue
		}
		res.Removed = append(res.Removed, img.Reference)
	}
	return res, nil
}

func removeImage(ref string) error {
	out, err := exec.Command("docker", "rmi", ref).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// PruneDangling removes dangling (<none>:<none>) images. Equivalent to
// `docker image prune -f`.
func PruneDangling() error {
	out, err := exec.Command("docker", "image", "prune", "-f").CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker image prune: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
