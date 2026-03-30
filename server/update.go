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
	image := fmt.Sprintf("ghcr.io/%s/poof:latest", strings.ToLower(s.cfg.GitHub.User))

	log.Printf("update: current version=%s commit=%s", version.Number, version.Commit)
	log.Printf("update: pulling %s", image)

	pullOut, err := docker.PullSelf(image, s.cfg.GitHub.User, s.cfg.GitHub.Token)
	if err != nil {
		jsonError(w, fmt.Sprintf("pull failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Log each line of the pull output (includes Status and Digest).
	for _, line := range strings.Split(pullOut, "\n") {
		if line != "" {
			log.Printf("update: pull: %s", line)
		}
	}

	// Inspect the pulled image for OCI labels so we can confirm what landed.
	labels := docker.InspectLabels(image)
	if len(labels) > 0 {
		log.Printf("update: image version=%s commit=%s created=%s",
			labels["org.opencontainers.image.version"],
			labels["org.opencontainers.image.revision"],
			labels["org.opencontainers.image.created"],
		)
	}

	log.Printf("update: pull complete, exiting for container restart")
	jsonOK(w, map[string]string{"status": "restarting"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		os.Exit(0)
	}()
}
