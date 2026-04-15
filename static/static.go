package static

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const maxVersions = 5

// Deploy extracts a tar.gz archive into a versioned directory and atomically
// swaps the "current" symlink to point to it.
func Deploy(dataDir, project string, depID int64, tarball io.Reader) error {
	base := projectDir(dataDir, project)
	versionsDir := filepath.Join(base, "versions")
	if err := os.MkdirAll(versionsDir, 0755); err != nil {
		return fmt.Errorf("create versions dir: %w", err)
	}

	// Extract into a temp directory first.
	tmp, err := os.MkdirTemp(versionsDir, ".tmp-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}

	if err := extractTarGz(tarball, tmp); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("extract archive: %w", err)
	}

	// Rename to final version directory.
	versionDir := filepath.Join(versionsDir, fmt.Sprintf("v%d", depID))
	if err := os.Rename(tmp, versionDir); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("rename to version dir: %w", err)
	}

	// Atomically swap the "current" symlink.
	currentLink := filepath.Join(base, "current")
	newLink := currentLink + ".new"
	os.Remove(newLink)
	if err := os.Symlink(versionDir, newLink); err != nil {
		return fmt.Errorf("create new symlink: %w", err)
	}
	if err := os.Rename(newLink, currentLink); err != nil {
		os.Remove(newLink)
		return fmt.Errorf("swap symlink: %w", err)
	}

	// Clean up old versions.
	pruneVersions(versionsDir)
	return nil
}

// Rollback re-points the "current" symlink to a previous version.
func Rollback(dataDir, project string, depID int64) error {
	base := projectDir(dataDir, project)
	versionDir := filepath.Join(base, "versions", fmt.Sprintf("v%d", depID))
	if _, err := os.Stat(versionDir); err != nil {
		return fmt.Errorf("version v%d not found on disk", depID)
	}

	currentLink := filepath.Join(base, "current")
	newLink := currentLink + ".new"
	os.Remove(newLink)
	if err := os.Symlink(versionDir, newLink); err != nil {
		return fmt.Errorf("create new symlink: %w", err)
	}
	if err := os.Rename(newLink, currentLink); err != nil {
		os.Remove(newLink)
		return fmt.Errorf("swap symlink: %w", err)
	}
	return nil
}

// IsDeployed returns true if the project has a "current" symlink.
func IsDeployed(dataDir, project string) bool {
	_, err := os.Stat(filepath.Join(projectDir(dataDir, project), "current"))
	return err == nil
}

// Remove deletes the entire project's static directory.
func Remove(dataDir, project string) {
	os.RemoveAll(projectDir(dataDir, project))
}

func projectDir(dataDir, project string) string {
	return filepath.Join(dataDir, "static", project)
}

func extractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Sanitize: prevent path traversal.
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") {
			continue
		}
		target := filepath.Join(dest, clean)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

func pruneVersions(versionsDir string) {
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return
	}

	// Collect version directories sorted by version number descending.
	type versionEntry struct {
		num  int64
		name string
	}
	var versions []versionEntry
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "v") {
			continue
		}
		n, err := strconv.ParseInt(e.Name()[1:], 10, 64)
		if err != nil {
			continue
		}
		versions = append(versions, versionEntry{num: n, name: e.Name()})
	}

	sort.Slice(versions, func(i, j int) bool {
		return versions[i].num > versions[j].num
	})

	// Remove versions beyond the limit.
	for i := maxVersions; i < len(versions); i++ {
		os.RemoveAll(filepath.Join(versionsDir, versions[i].name))
	}
}
