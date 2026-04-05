package server

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/racso/poof/docker"
	"github.com/racso/poof/version"
)

func (s *Server) updateServer(w http.ResponseWriter, r *http.Request) {
	image := fmt.Sprintf("ghcr.io/%s/poof:latest", strings.ToLower(s.settingGitHubUser()))

	log.Printf("update: current version=%s commit=%s", version.Number, version.Commit)
	log.Printf("update: pulling %s", image)

	pullOut, err := docker.PullSelf(image, s.settingGitHubUser(), s.settingGitHubToken())
	if err != nil {
		jsonError(w, fmt.Sprintf("pull failed: %v", err), http.StatusInternalServerError)
		return
	}

	for _, line := range strings.Split(pullOut, "\n") {
		if line != "" {
			log.Printf("update: pull: %s", line)
		}
	}

	labels := docker.InspectLabels(image)
	if len(labels) > 0 {
		log.Printf("update: image version=%s commit=%s created=%s",
			labels["org.opencontainers.image.version"],
			labels["org.opencontainers.image.revision"],
			labels["org.opencontainers.image.created"],
		)
	}

	// Identify our own container name so the helper can replace us.
	containerName, err := docker.SelfContainerName()
	if err != nil {
		// Not running in Docker (e.g. local dev). Fall back to plain exit.
		log.Printf("update: not in a container (%v) — exiting for process restart", err)
		jsonOK(w, map[string]string{"status": "restarting"})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func() {
			time.Sleep(200 * time.Millisecond)
			os.Exit(0)
		}()
		return
	}

	// Launch a disposable helper container that will — after we exit — stop
	// this container, remove it, and start a fresh one with the new image.
	// This is necessary because Docker's --restart policy replays the original
	// image ID; only a stop+rm+run cycle picks up the newly pulled image.
	log.Printf("update: launching helper to replace container %q", containerName)
	if err := docker.ReplaceSelf(containerName, image); err != nil {
		jsonError(w, fmt.Sprintf("failed to launch replacement helper: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("update: helper launched, exiting")
	jsonOK(w, map[string]string{"status": "restarting"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		os.Exit(0)
	}()
}
