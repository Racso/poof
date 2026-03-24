package server

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/racso/poof/docker"
)

func (s *Server) updateServer(w http.ResponseWriter, r *http.Request) {
	image := fmt.Sprintf("ghcr.io/%s/poof:latest", s.cfg.GitHub.User)

	log.Printf("update: pulling %s", image)
	if err := docker.PullSelf(image, s.cfg.GitHub.User, s.cfg.GitHub.Token); err != nil {
		jsonError(w, fmt.Sprintf("pull failed: %v", err), http.StatusInternalServerError)
		return
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
