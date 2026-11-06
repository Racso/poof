package cmd

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var deployCmd = &cobra.Command{
	Use:   "deploy <name>",
	Short: "Redeploy a project's latest version, or upload a static site with --from",
	Long: `Redeploy a project.

Container projects: redeploys the latest recorded image, or --image <tag>.

Static projects: re-points the site at its latest successful version. Nothing
is uploaded; CI builds and uploads new versions on push. To publish a local
folder instead, pass --from <dir>. The folder must contain an index.html at its
top level unless --no-index is given; .git is never uploaded.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		image, _ := cmd.Flags().GetString("image")
		from, _ := cmd.Flags().GetString("from")
		noIndex, _ := cmd.Flags().GetBool("no-index")

		// Fetch project info to determine type.
		var info struct {
			Project struct {
				Static string `json:"static"`
			} `json:"project"`
		}
		if err := apiGet("/projects/"+name, &info); err != nil {
			fatal("%v", err)
		}

		isStatic := info.Project.Static == "static" || info.Project.Static == "spa"

		if isStatic && image != "" {
			fatal("--image does not apply to a static project (use --from <dir> to upload a folder)")
		}
		if !isStatic && (from != "" || noIndex) {
			fatal("--from and --no-index only apply to static projects")
		}
		if noIndex && from == "" {
			fatal("--no-index only applies together with --from")
		}
		if isStatic && from != "" {
			deployStatic(name, from, noIndex)
			return
		}

		payload := map[string]interface{}{}
		if image != "" {
			payload["image"] = image
		}

		var result map[string]interface{}
		if err := apiPost("/projects/"+name+"/deploy", payload, &result); err != nil {
			fatal("%v", err)
		}

		if result["status"] == "staged" {
			fmt.Printf("✓ staged %q (paused — container replaced but not started)\n", name)
			fmt.Printf("  run `poof resume %s` to start it\n", name)
			return
		}

		if v, ok := result["version"].(float64); ok {
			fmt.Printf("✓ redeployed %q (static, v%d)\n", name, int64(v))
		} else {
			fmt.Printf("✓ deployed %q\n", name)
		}
		if d, ok := result["domain"].(string); ok {
			fmt.Printf("  https://%s\n", d)
		}
	},
}

// checkStaticDir refuses a folder that is not a directory or, unless noIndex,
// has no index.html at its top level: the guard against publishing whatever
// directory the command happened to be run from. It returns the number of
// files and bytes that would be uploaded, .git excluded.
func checkStaticDir(dir string, noIndex bool) (files int, size int64, err error) {
	stat, err := os.Stat(dir)
	if err != nil || !stat.IsDir() {
		return 0, 0, fmt.Errorf("directory %q does not exist", dir)
	}
	if !noIndex {
		if fi, err := os.Stat(filepath.Join(dir, "index.html")); err != nil || !fi.Mode().IsRegular() {
			return 0, 0, fmt.Errorf("%q has no index.html at its top level, so it does not look like a site; pass --no-index if that is intended", dir)
		}
	}
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if info.Mode().IsRegular() {
			files++
			size += info.Size()
		}
		return nil
	})
	return files, size, err
}

func deployStatic(name, dir string, noIndex bool) {
	files, size, err := checkStaticDir(dir, noIndex)
	if err != nil {
		fatal("%v", err)
	}
	abs, _ := filepath.Abs(dir)
	fmt.Printf("uploading %s: %d files, %s\n", abs, files, humanBytes(size))

	// Create tar.gz in memory using a pipe.
	pr, pw := io.Pipe()

	go func() {
		gw := gzip.NewWriter(pw)
		tw := tar.NewWriter(gw)

		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// Make paths relative to the source dir.
			relPath, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			if relPath == "." {
				return nil
			}
			if info.IsDir() && info.Name() == ".git" {
				return filepath.SkipDir
			}

			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = relPath

			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}

			if !info.Mode().IsRegular() {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			_, err = io.Copy(tw, f)
			return err
		})

		_ = tw.Close()
		_ = gw.Close()
		pw.CloseWithError(err)
	}()

	// Upload the tarball.
	url := serverURL() + "/projects/" + name + "/deploy/static"
	req, err := http.NewRequest("POST", url, pr)
	if err != nil {
		fatal("%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken())
	req.Header.Set("Content-Type", "application/gzip")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("upload failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e map[string]string
		if json.Unmarshal(body, &e) == nil {
			fatal("server error: %s", e["error"])
		}
		fatal("server returned %s", resp.Status)
	}

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	fmt.Printf("✓ deployed %q (static)\n", name)
	if d, ok := result["domain"].(string); ok {
		fmt.Printf("  https://%s\n", d)
	}
}

func init() {
	rootCmd.AddCommand(deployCmd)
	deployCmd.Flags().String("image", "", "container projects: image to deploy (default: latest recorded)")
	deployCmd.Flags().String("from", "", "static projects: upload this local folder as a new version")
	deployCmd.Flags().Bool("no-index", false, "with --from: allow a folder without index.html")
}
