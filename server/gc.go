package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/racso/poof/store"
)

// gcRequest is the body of POST /gc.
//
//	{"project": "x"}                            — GC one project using its policy
//	{"all": true}                               — GC every project using each policy
//	{"project": "x", "keep": 5}                 — manual override (just for this run)
//	{"project": "x", "older_than_days": 14}
//	{"all": true, "dry_run": true}
type gcRequest struct {
	Project       string `json:"project"`
	All           bool   `json:"all"`
	Keep          *int   `json:"keep,omitempty"`
	OlderThanDays *int   `json:"older_than_days,omitempty"`
	DryRun        bool   `json:"dry_run,omitempty"`
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

	override := req.Keep != nil || req.OlderThanDays != nil

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

	var results []GCResult
	for _, p := range projects {
		var keep, older int
		if override {
			if req.Keep != nil {
				keep = *req.Keep
			}
			if req.OlderThanDays != nil {
				older = *req.OlderThanDays
			}
		} else {
			policy, enabled := s.store.ResolveGCPolicy(p.Name)
			if !enabled {
				continue
			}
			if policy.KeepCount != nil {
				keep = *policy.KeepCount
			}
			if policy.OlderThanDays != nil {
				older = *policy.OlderThanDays
			}
		}

		if p.IsStatic() {
			res, err := s.runStaticGC(p.Name, keep, older, req.DryRun)
			if err != nil {
				log.Printf("gc %s (static) failed: %v", p.Name, err)
			} else {
				results = append(results, res)
			}
		} else if p.Image != "" {
			res, err := s.container.GC(p.Name, p.Image, keep, older, req.DryRun)
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

// gcStatus handles GET /gc/status — returns all stored policies plus the
// resolved policy per project.
func (s *Server) gcStatus(w http.ResponseWriter, r *http.Request) {
	policies, err := s.store.ListGCPolicies()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	projects, err := s.store.ListProjects()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type resolved struct {
		Project       string `json:"project"`
		KeepCount     *int   `json:"keep_count,omitempty"`
		OlderThanDays *int   `json:"older_than_days,omitempty"`
		Enabled       bool   `json:"enabled"`
		Source        string `json:"source"` // "project", "global", "default", "disabled"
	}

	resolvedList := make([]resolved, 0, len(projects))
	for _, p := range projects {
		r := resolved{Project: p.Name}
		if pol, _ := s.store.GetGCPolicy(p.Name); pol != nil {
			if pol.Disabled {
				r.Enabled = false
				r.Source = "disabled"
			} else {
				r.KeepCount = pol.KeepCount
				r.OlderThanDays = pol.OlderThanDays
				r.Enabled = true
				r.Source = "project"
			}
		} else if g, _ := s.store.GetGCPolicy(store.GCPolicyGlobalKey); g != nil {
			if g.Disabled {
				r.Enabled = false
				r.Source = "disabled"
			} else {
				r.KeepCount = g.KeepCount
				r.OlderThanDays = g.OlderThanDays
				r.Enabled = true
				r.Source = "global"
			}
		} else {
			def := 3
			r.KeepCount = &def
			r.Enabled = true
			r.Source = "default"
		}
		resolvedList = append(resolvedList, r)
	}

	jsonOK(w, map[string]interface{}{
		"policies": policies,
		"resolved": resolvedList,
	})
}

type gcPolicyRequest struct {
	KeepCount     *int `json:"keep_count,omitempty"`
	OlderThanDays *int `json:"older_than_days,omitempty"`
	Disabled      bool `json:"disabled,omitempty"`
}

// setGCPolicy handles PUT /gc/policy/{name}. Use "_default" as the path value
// to set the global default policy.
func (s *Server) setGCPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	target := name
	if name == "_default" {
		target = store.GCPolicyGlobalKey
	} else {
		p, err := s.store.GetProject(name)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if p == nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}
	}

	var req gcPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if !req.Disabled && req.KeepCount == nil && req.OlderThanDays == nil {
		jsonError(w, "at least one of keep_count, older_than_days, or disabled is required", http.StatusBadRequest)
		return
	}
	if req.KeepCount != nil && *req.KeepCount < 0 {
		jsonError(w, "keep_count must be >= 0", http.StatusBadRequest)
		return
	}
	if req.OlderThanDays != nil && *req.OlderThanDays < 0 {
		jsonError(w, "older_than_days must be >= 0", http.StatusBadRequest)
		return
	}

	policy := store.GCPolicy{
		Project:       target,
		KeepCount:     req.KeepCount,
		OlderThanDays: req.OlderThanDays,
		Disabled:      req.Disabled,
	}
	if err := s.store.SetGCPolicy(policy); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("gc policy set: %s (keep=%v older_than=%v disabled=%v)",
		target, req.KeepCount, req.OlderThanDays, req.Disabled)
	jsonOK(w, policy)
}

// deleteGCPolicy handles DELETE /gc/policy/{name}.
func (s *Server) deleteGCPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	target := name
	if name == "_default" {
		target = store.GCPolicyGlobalKey
	}
	if err := s.store.DeleteGCPolicy(target); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("gc policy removed: %s", target)
	jsonOK(w, map[string]string{"status": "removed"})
}

// runAutoGC iterates every container project, applies its resolved GC policy,
// and prunes dangling images. Designed to be invoked from a goroutine after
// every successful deploy.
func (s *Server) runAutoGC() {
	projects, err := s.store.ListProjects()
	if err != nil {
		log.Printf("auto-gc: list projects: %v", err)
		return
	}

	beforeBytes, beforeErr := s.container.ImagesDiskUsage()

	var anyRan bool
	for _, p := range projects {
		policy, enabled := s.store.ResolveGCPolicy(p.Name)
		if !enabled {
			continue
		}
		var keep, older int
		if policy.KeepCount != nil {
			keep = *policy.KeepCount
		}
		if policy.OlderThanDays != nil {
			older = *policy.OlderThanDays
		}
		if keep == 0 && older == 0 {
			continue
		}

		if p.IsStatic() {
			res, err := s.runStaticGC(p.Name, keep, older, false)
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
			res, err := s.container.GC(p.Name, p.Image, keep, older, false)
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
func (s *Server) runStaticGC(project string, keep, olderThanDays int, dryRun bool) (GCResult, error) {
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
	return s.static.GC(s.cfg.DataDir, project, versions, keep, olderThanDays, dryRun)
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

