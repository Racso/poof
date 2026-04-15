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
	"strings"

	"github.com/spf13/cobra"
)

var deployCmd = &cobra.Command{
	Use:   "deploy <name>",
	Short: "Trigger a manual deploy (uses latest recorded image)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		image, _ := cmd.Flags().GetString("image")

		// Fetch project info to determine type.
		var info struct {
			Project struct {
				Static string `json:"static"`
				Folder string `json:"folder"`
			} `json:"project"`
		}
		if err := apiGet("/projects/"+name, &info); err != nil {
			fatal("%v", err)
		}

		isStatic := info.Project.Static == "static" || info.Project.Static == "spa"

		if isStatic {
			deployStatic(name, info.Project.Folder)
			return
		}

		// Container deploy (existing behavior).
		payload := map[string]interface{}{}
		if image != "" {
			payload["image"] = image
		}

		var result map[string]interface{}
		if err := apiPost("/projects/"+name+"/deploy", payload, &result); err != nil {
			fatal("%v", err)
		}

		fmt.Printf("✓ deployed %q\n", name)
		if d, ok := result["domain"].(string); ok {
			fmt.Printf("  https://%s\n", d)
		}
	},
}

func deployStatic(name, folder string) {
	// Determine directory to tar.
	dir := "."
	if folder != "" {
		dir = strings.TrimRight(folder, "/")
	}

	// Verify directory exists.
	stat, err := os.Stat(dir)
	if err != nil || !stat.IsDir() {
		fatal("directory %q does not exist", dir)
	}

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
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})

		tw.Close()
		gw.Close()
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
	defer resp.Body.Close()

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
	deployCmd.Flags().String("image", "", "specific image to deploy (default: latest recorded)")
}
