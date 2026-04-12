package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update the Poof! CLI binary and/or server",
}

var updateLocalCmd = &cobra.Command{
	Use:   "local [version]",
	Short: "Update the local poof binary",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		doUpdateLocal(optionalVersion(args))
	},
}

var updateServerCmd = &cobra.Command{
	Use:   "server [version]",
	Short: "Update the remote Poof! server",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		doUpdateServer(optionalVersion(args))
	},
}

var updateBothCmd = &cobra.Command{
	Use:   "both [version]",
	Short: "Update server first, then local CLI",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		v := optionalVersion(args)
		fmt.Println("==> Updating server...")
		doUpdateServer(v)
		fmt.Println()
		fmt.Println("==> Updating local CLI...")
		doUpdateLocal(v)
	},
}

func optionalVersion(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

// normalizeTag ensures a version string has the "v" prefix used by releases.
func normalizeTag(version string) string {
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func doUpdateLocal(version string) {
	var tag string
	if version != "" {
		tag = normalizeTag(version)
		fmt.Printf("Target version: %s\n", tag)
	} else {
		fmt.Println("Checking latest release...")
		var err error
		tag, err = latestReleaseTag()
		if err != nil {
			fatal("could not fetch latest release: %v", err)
		}
		fmt.Printf("Latest release: %s\n", tag)
	}

	goos := runtime.GOOS
	goarch := runtime.GOARCH
	url := fmt.Sprintf(
		"https://github.com/Racso/poof/releases/download/%s/poof-%s-%s",
		tag, goos, goarch,
	)

	exe, err := os.Executable()
	if err != nil {
		fatal("could not determine executable path: %v", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		fatal("could not resolve executable path: %v", err)
	}

	fmt.Printf("Downloading %s/%s...\n", goos, goarch)
	tmp, err := downloadTo(filepath.Dir(exe), url)
	if err != nil {
		fatal("%v", err)
	}

	if err := os.Chmod(tmp, 0755); err != nil {
		os.Remove(tmp)
		fatal("could not set permissions: %v", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		if os.IsPermission(err) {
			fatal("permission denied writing to %s — try running with sudo", exe)
		}
		fatal("could not replace binary: %v", err)
	}

	fmt.Printf("Updated local CLI to %s\n", tag)
}

func doUpdateServer(version string) {
	var payload map[string]string
	if version != "" {
		tag := normalizeTag(version)
		fmt.Printf("Updating server to %s...\n", tag)
		payload = map[string]string{"version": tag}
	} else {
		fmt.Println("Updating server to latest...")
	}

	var result map[string]string
	if err := apiPost("/update", payload, &result); err != nil {
		fatal("%v", err)
	}
	fmt.Println("Server updating — restarting now")
}

func init() {
	rootCmd.AddCommand(updateCmd)
	updateCmd.AddCommand(updateLocalCmd)
	updateCmd.AddCommand(updateServerCmd)
	updateCmd.AddCommand(updateBothCmd)
}
