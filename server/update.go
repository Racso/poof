package server

import "net/http"

// updateServer is temporarily disabled.
// update-remote has a known bug: Docker's --restart policy replays the
// original image ID, so pulling :latest and exiting does not actually
// switch to the new image. A proper fix (container self-replacement via
// a helper container) is pending.
func (s *Server) updateServer(w http.ResponseWriter, r *http.Request) {
	jsonError(w, "update-remote is temporarily disabled — use 'docker compose pull && docker compose up -d' on the server", http.StatusServiceUnavailable)
}
