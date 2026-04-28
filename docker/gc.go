package docker

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
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

// selectForRemoval applies the keep/age filters and returns which references
// should be deleted vs kept. It is pure: no docker calls, deterministic given
// inputs. Behavior:
//   - Images are sorted newest first before filtering.
//   - Any image whose ID matches runningID is always kept.
//   - When both keep and olderThanDays are >0, an image must satisfy BOTH
//     conditions to be deleted (outside keep window AND older than N days).
//   - When only one is set, that condition alone governs deletion.
//   - When both are 0, nothing is deleted.
func selectForRemoval(images []LocalImage, runningID string, keep, olderThanDays int, now time.Time) (toDelete, toKeep []LocalImage) {
	if keep <= 0 && olderThanDays <= 0 {
		return nil, images
	}

	sorted := make([]LocalImage, len(images))
	copy(sorted, images)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Created.After(sorted[j].Created)
	})

	var cutoff time.Time
	if olderThanDays > 0 {
		cutoff = now.AddDate(0, 0, -olderThanDays)
	}

	for i, img := range sorted {
		if runningID != "" && img.ID == runningID {
			toKeep = append(toKeep, img)
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

		if eligible {
			toDelete = append(toDelete, img)
		} else {
			toKeep = append(toKeep, img)
		}
	}
	return toDelete, toKeep
}

// GC removes old images for a project according to the given retention rules.
// See selectForRemoval for the filtering semantics. dryRun=true skips the
// docker rmi calls but still reports what would have been removed.
func GC(projectName, image string, keep, olderThanDays int, dryRun bool) (GCResult, error) {
	res := GCResult{Project: projectName}
	if keep <= 0 && olderThanDays <= 0 {
		return res, nil
	}

	images, err := listImagesForRepo(image)
	if err != nil {
		return res, err
	}

	toDelete, toKeep := selectForRemoval(images, runningImageID(projectName), keep, olderThanDays, time.Now())

	for _, img := range toKeep {
		res.Kept = append(res.Kept, img.Reference)
	}
	for _, img := range toDelete {
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

// ImagesDiskUsage returns the total bytes used by Docker images on this host,
// per `docker system df`. The figure is layer-sharing-aware (it is the on-disk
// size, not the sum of per-image apparent sizes).
func ImagesDiskUsage() (int64, error) {
	out, err := exec.Command(
		"docker", "system", "df", "--format", "{{.Type}}\t{{.Size}}",
	).Output()
	if err != nil {
		return 0, fmt.Errorf("docker system df: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) == "Images" {
			return parseHumanSize(parts[1])
		}
	}
	return 0, fmt.Errorf("docker system df: no Images row")
}

// parseHumanSize parses Docker's humanized sizes (SI: 1 kB = 1000 bytes), e.g.
// "0B", "412kB", "5.2GB", "1.5MB". Returns bytes.
func parseHumanSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("parse size %q: no number", s)
	}
	num, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, fmt.Errorf("parse size %q: %w", s, err)
	}
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	var mult float64
	switch unit {
	case "", "b":
		mult = 1
	case "kb":
		mult = 1e3
	case "mb":
		mult = 1e6
	case "gb":
		mult = 1e9
	case "tb":
		mult = 1e12
	case "pb":
		mult = 1e15
	default:
		return 0, fmt.Errorf("parse size %q: unknown unit %q", s, unit)
	}
	return int64(num * mult), nil
}

// runningImageIDs returns the set of image IDs used by all running containers.
func runningImageIDs() map[string]bool {
	out, err := exec.Command(
		"docker", "ps", "-q",
	).Output()
	if err != nil {
		return nil
	}

	ids := make(map[string]bool)
	for _, cid := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if cid == "" {
			continue
		}
		imgOut, err := exec.Command(
			"docker", "inspect", "-f", "{{.Image}}", cid,
		).Output()
		if err != nil {
			continue
		}
		ids[strings.TrimSpace(string(imgOut))] = true
	}
	return ids
}

// imageID returns the image ID for a reference, or "" if not found locally.
func imageID(ref string) string {
	out, err := exec.Command(
		"docker", "inspect", "-f", "{{.Id}}", ref,
	).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SweepOrphans removes image references that are present locally but not used
// by any running container. Designed for cleaning up images from deleted or
// static-converted projects. dryRun=true reports what would be removed.
func SweepOrphans(refs []string, dryRun bool) (GCResult, error) {
	res := GCResult{Project: "(orphans)"}
	if len(refs) == 0 {
		return res, nil
	}

	running := runningImageIDs()

	for _, ref := range refs {
		id := imageID(ref)
		if id == "" {
			continue // Not on disk.
		}
		if running[id] {
			res.Kept = append(res.Kept, ref)
			continue
		}
		if dryRun {
			res.Removed = append(res.Removed, ref)
			continue
		}
		if err := removeImage(ref); err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", ref, err))
		} else {
			res.Removed = append(res.Removed, ref)
		}
	}
	return res, nil
}
