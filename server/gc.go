package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/racso/poof/store"
)

const (
	// defaultGCQuietPeriod is how long the host must go without a deploy
	// before an automatic sweep starts. A push to main typically deploys two
	// projects (test, then prod) seconds apart from the same image; waiting
	// for quiet collapses those into a single sweep after both have landed,
	// instead of firing one into the gap between them.
	defaultGCQuietPeriod = 30 * time.Second

	// gcQuietAttempts bounds how many quiet periods a pending automatic sweep
	// will wait through before giving up. The next deploy re-requests it.
	gcQuietAttempts = 10

	// manualGCWait bounds how long an operator-requested GC waits for
	// in-flight deploys before giving up, so a hung pull cannot wedge it (and,
	// with it, every subsequent deploy) forever.
	manualGCWait = 2 * time.Minute
)

// gcRequest is the body of POST /gc.
//
//	{"project": "x"}                — GC one project using the global policy
//	{"all": true}                   — GC every project
//	{"project": "x", "keep": 5}     — manual override (just for this run)
//	{"all": true, "dry_run": true}
type gcRequest struct {
	Project string `json:"project"`
	All     bool   `json:"all"`
	Keep    *int   `json:"keep,omitempty"`
	DryRun  bool   `json:"dry_run,omitempty"`
}

// triggerGC handles POST /gc — manual GC.
func (s *Server) triggerGC(w http.ResponseWriter, r *http.Request) {
	var req gcRequest
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}
	if !req.All && req.Project == "" {
		jsonError(w, "either project or all is required", http.StatusBadRequest)
		return
	}

	// A manual GC was explicitly asked for, so it waits for in-flight deploys
	// rather than skipping. Deploys arriving from here on queue behind it.
	// Dry runs touch nothing and need no gate.
	if !req.DryRun {
		ctx, cancel := context.WithTimeout(r.Context(), manualGCWait)
		defer cancel()
		if err := s.gate.enterGC(ctx); err != nil {
			jsonError(w, "timed out waiting for in-flight deploys to finish", http.StatusServiceUnavailable)
			return
		}
		defer s.gate.leaveGC()
	}

	var projects []store.Project
	if req.All {
		ps, err := s.store.ListProjects()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		projects = ps
	} else {
		p, err := s.store.GetProject(req.Project)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if p == nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}
		projects = []store.Project{*p}
	}

	var beforeBytes int64
	var measured bool
	if !req.DryRun {
		if b, err := s.container.ImagesDiskUsage(); err == nil {
			beforeBytes = b
			measured = true
		} else {
			log.Printf("warning: docker system df before GC failed: %v", err)
		}
	}

	keep, enabled := s.resolveGCKeep(req.Keep)
	if !enabled {
		jsonOK(w, map[string]interface{}{"results": []GCResult{}, "dry_run": req.DryRun, "disabled": true})
		return
	}

	var results []GCResult
	for _, p := range projects {
		if p.IsStatic() {
			res, err := s.runStaticGC(p.Name, keep, req.DryRun)
			if err != nil {
				log.Printf("gc %s (static) failed: %v", p.Name, err)
			} else {
				results = append(results, res)
			}
		} else if p.Image != "" {
			res, err := s.container.GC(p.Name, p.Image, keep, req.DryRun)
			if err != nil {
				log.Printf("gc %s failed: %v", p.Name, err)
				res.Project = p.Name
			}
			results = append(results, res)
		}
	}

	// Orphan sweep: clean up images from deleted or static-converted projects.
	if req.All {
		if orphanRefs, err := s.store.ListOrphanDeploymentImages(); err != nil {
			log.Printf("gc orphan query: %v", err)
		} else if len(orphanRefs) > 0 {
			res, err := s.container.SweepOrphans(orphanRefs, req.DryRun)
			if err != nil {
				log.Printf("gc orphan sweep: %v", err)
			} else {
				results = append(results, res)
			}
		}
	}

	resp := map[string]interface{}{
		"results": results,
		"dry_run": req.DryRun,
	}

	if !req.DryRun {
		if err := s.container.PruneDangling(); err != nil {
			log.Printf("warning: prune dangling images failed: %v", err)
		}
		if measured {
			afterBytes, err := s.container.ImagesDiskUsage()
			if err != nil {
				log.Printf("warning: docker system df after GC failed: %v", err)
			} else {
				freed := beforeBytes - afterBytes
				if freed < 0 {
					freed = 0
				}
				resp["bytes_freed"] = freed
			}
		}
	}

	jsonOK(w, resp)
}

// resolveGCKeep returns the retention to use for this run. An explicit
// override wins and bypasses the disabled flag (the operator asked for it by
// hand); otherwise the global config governs.
func (s *Server) resolveGCKeep(override *int) (keep int, enabled bool) {
	if override != nil {
		return *override, true
	}
	c := s.store.GetGCConfig()
	if c.Disabled {
		return 0, false
	}
	return c.Keep, true
}

// gcStatus handles GET /gc/status — returns the global retention policy.
func (s *Server) gcStatus(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.store.GetGCConfig())
}

type gcConfigRequest struct {
	Keep     *int  `json:"keep,omitempty"`
	Disabled *bool `json:"disabled,omitempty"`
}

// setGCConfig handles PUT /gc/config — sets the global retention policy.
// Fields left out are unchanged, so turning GC back on never loses the count.
func (s *Server) setGCConfig(w http.ResponseWriter, r *http.Request) {
	var req gcConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Keep == nil && req.Disabled == nil {
		jsonError(w, "at least one of keep or disabled is required", http.StatusBadRequest)
		return
	}
	if req.Keep != nil && *req.Keep < 0 {
		jsonError(w, "keep must be >= 0", http.StatusBadRequest)
		return
	}

	c := s.store.GetGCConfig()
	if req.Keep != nil {
		c.Keep = *req.Keep
	}
	if req.Disabled != nil {
		c.Disabled = *req.Disabled
	}
	if err := s.store.SetGCConfig(c); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("gc config set: keep=%d disabled=%v", c.Keep, c.Disabled)
	jsonOK(w, c)
}

// requestAutoGC asks the GC worker for a sweep. Non-blocking: if a request is
// already pending it is left as is, since one sweep covers every project.
func (s *Server) requestAutoGC() {
	select {
	case s.gcSignal <- struct{}{}:
	default:
	}
}

// gcWorker owns every automatic sweep. Running them through a single worker
// (rather than a goroutine per deploy) means concurrent deploys can never
// produce concurrent collectors, and back-to-back deploys coalesce into one
// sweep.
func (s *Server) gcWorker() {
	for range s.gcSignal {
		s.autoGCWhenQuiet()
	}
}

// autoGCWhenQuiet waits for a deploy-free window, then sweeps under the gate.
// Automatic GC is opportunistic: it never makes a deploy wait for it.
func (s *Server) autoGCWhenQuiet() {
	quiet := s.gcQuiet
	if quiet <= 0 {
		quiet = defaultGCQuietPeriod
	}
	for attempt := 0; attempt < gcQuietAttempts; attempt++ {
		time.Sleep(quiet)
		if !s.gate.tryEnterGC() {
			continue // a deploy is in flight — wait for the next quiet window
		}
		s.gcSweep()
		s.gate.leaveGC()
		// The sweep covered every project, so a request raised while it was
		// pending or running is already satisfied.
		select {
		case <-s.gcSignal:
		default:
		}
		return
	}
	log.Printf("auto-gc: still no quiet window after %d attempts — skipping; the next deploy will request another sweep", gcQuietAttempts)
}

// runAutoGC iterates every container project, applies its resolved GC policy,
// and prunes dangling images. Designed to be invoked from a goroutine after
// every successful deploy. Callers must hold the deploy gate.
func (s *Server) runAutoGC() {
	projects, err := s.store.ListProjects()
	if err != nil {
		log.Printf("auto-gc: list projects: %v", err)
		return
	}

	beforeBytes, beforeErr := s.container.ImagesDiskUsage()

	cfg := s.store.GetGCConfig()
	if cfg.Disabled || cfg.Keep <= 0 {
		return
	}
	keep := cfg.Keep

	var anyRan bool
	for _, p := range projects {
		if p.IsStatic() {
			res, err := s.runStaticGC(p.Name, keep, false)
			if err != nil {
				log.Printf("auto-gc %s (static): %v", p.Name, err)
				continue
			}
			anyRan = true
			if len(res.Removed) > 0 || len(res.Failed) > 0 {
				log.Printf("auto-gc %s (static): removed=%d failed=%d",
					p.Name, len(res.Removed), len(res.Failed))
			}
		} else if p.Image != "" {
			res, err := s.container.GC(p.Name, p.Image, keep, false)
			if err != nil {
				log.Printf("auto-gc %s: %v", p.Name, err)
				continue
			}
			anyRan = true
			if len(res.Removed) > 0 || len(res.Failed) > 0 {
				log.Printf("auto-gc %s: removed=%d kept=%d failed=%d",
					p.Name, len(res.Removed), len(res.Kept), len(res.Failed))
			}
		}
	}

	// Orphan sweep.
	if orphanRefs, err := s.store.ListOrphanDeploymentImages(); err != nil {
		log.Printf("auto-gc orphan query: %v", err)
	} else if len(orphanRefs) > 0 {
		res, err := s.container.SweepOrphans(orphanRefs, false)
		if err != nil {
			log.Printf("auto-gc orphan sweep: %v", err)
		} else {
			anyRan = true
			if len(res.Removed) > 0 {
				log.Printf("auto-gc orphans: removed=%d", len(res.Removed))
			}
		}
	}

	if anyRan {
		if err := s.container.PruneDangling(); err != nil {
			log.Printf("auto-gc prune dangling: %v", err)
		}
		if beforeErr == nil {
			if afterBytes, err := s.container.ImagesDiskUsage(); err == nil {
				freed := beforeBytes - afterBytes
				if freed < 0 {
					freed = 0
				}
				log.Printf("auto-gc: freed %s", humanBytes(freed))
			}
		}
	}
}

// runStaticGC queries deployment history for a static project and delegates
// to the StaticDeployer.GC method.
func (s *Server) runStaticGC(project string, keep int, dryRun bool) (GCResult, error) {
	deps, err := s.store.ListDeployments(project, 0)
	if err != nil {
		return GCResult{}, fmt.Errorf("list deployments: %w", err)
	}
	var versions []StaticVersion
	for _, d := range deps {
		if d.Status == "success" {
			versions = append(versions, StaticVersion{DepID: d.ID, DeployedAt: d.DeployedAt})
		}
	}
	return s.static.GC(s.cfg.DataDir, project, versions, keep, dryRun)
}

// humanBytes formats a byte count with SI units (matching docker's output).
func humanBytes(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	const k = 1000.0
	units := []string{"kB", "MB", "GB", "TB", "PB"}
	v := float64(n) / k
	u := 0
	for v >= k && u < len(units)-1 {
		v /= k
		u++
	}
	return fmt.Sprintf("%.1f %s", v, units[u])
}
