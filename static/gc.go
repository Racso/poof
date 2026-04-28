package static

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// VersionInfo provides the deployment date for a static version so GC can
// apply the older-than policy. Supplied by the caller (from the store).
type VersionInfo struct {
	DepID      int64
	DeployedAt time.Time
}

// GCResult summarizes what static GC did for a project.
type GCResult struct {
	Project string   `json:"project"`
	Removed []string `json:"removed,omitempty"`
	Kept    []string `json:"kept,omitempty"`
	Failed  []string `json:"failed,omitempty"`
}

// GC removes old tarballs and extracted directories for a static project.
//
// Tarballs follow the keep/older-than policy (same semantics as Docker image
// GC). Extracted directories are pruned aggressively — only the current
// version's directory is kept; older versions can be re-extracted from their
// tarball on rollback.
//
// The currently serving version (from the "current" symlink) is never deleted.
func GC(dataDir, project string, versions []VersionInfo, keep, olderThanDays int, dryRun bool) (GCResult, error) {
	res := GCResult{Project: project}
	if keep <= 0 && olderThanDays <= 0 {
		return res, nil
	}

	base := projectDir(dataDir, project)
	versionsDir := filepath.Join(base, "versions")

	currentDep := currentVersionID(dataDir, project)

	// Build a date index from the version info.
	dateOf := make(map[int64]time.Time, len(versions))
	for _, v := range versions {
		dateOf[v.DepID] = v.DeployedAt
	}

	// Sort versions newest first by deployed_at.
	sorted := make([]VersionInfo, len(versions))
	copy(sorted, versions)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].DeployedAt.After(sorted[j].DeployedAt)
	})

	// Select tarballs for removal (same logic as Docker selectForRemoval).
	var cutoff time.Time
	if olderThanDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -olderThanDays)
	}

	removeTarballs := make(map[int64]bool)
	for i, v := range sorted {
		tar := fmt.Sprintf("v%d.tar.gz", v.DepID)

		if v.DepID == currentDep {
			res.Kept = append(res.Kept, tar)
			continue
		}

		outsideKeep := keep > 0 && i >= keep
		olderThan := !cutoff.IsZero() && v.DeployedAt.Before(cutoff)

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
			removeTarballs[v.DepID] = true
			tarPath := filepath.Join(versionsDir, tar)
			if dryRun {
				if fileExists(tarPath) {
					res.Removed = append(res.Removed, tar)
				}
			} else if fileExists(tarPath) {
				if err := os.Remove(tarPath); err != nil {
					res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", tar, err))
				} else {
					res.Removed = append(res.Removed, tar)
				}
			}
		} else {
			res.Kept = append(res.Kept, tar)
		}
	}

	// Prune extracted directories: remove all except current.
	entries, _ := os.ReadDir(versionsDir)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "v") {
			continue
		}
		n, err := strconv.ParseInt(e.Name()[1:], 10, 64)
		if err != nil {
			continue
		}
		if n == currentDep {
			continue
		}
		dirPath := filepath.Join(versionsDir, e.Name())
		dirLabel := e.Name() + "/"
		if dryRun {
			res.Removed = append(res.Removed, dirLabel)
		} else {
			if err := os.RemoveAll(dirPath); err != nil {
				res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", dirLabel, err))
			} else {
				res.Removed = append(res.Removed, dirLabel)
			}
		}
	}

	return res, nil
}

// currentVersionID reads the "current" symlink and returns the deployment ID,
// or 0 if no current version exists.
func currentVersionID(dataDir, project string) int64 {
	link := filepath.Join(projectDir(dataDir, project), "current")
	target, err := os.Readlink(link)
	if err != nil {
		return 0
	}
	base := filepath.Base(target)
	if !strings.HasPrefix(base, "v") {
		return 0
	}
	id, err := strconv.ParseInt(base[1:], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
