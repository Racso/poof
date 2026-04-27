# Garbage Collection & Enhanced Rollback

Design document for image garbage collection (`poof gc`) and time-based rollback (`poof rollback --before`).

---

## Problem

Every deploy pulls a new Docker image. Old images are never deleted. After enough deploys, disk fills up with hundreds of unused images (we observed 342 images / 41 GB on a single server with 27 running containers).

Meanwhile, rollback only supports "go back one version." There's no way to roll back to a specific point in time, and no resilience if the target image has been garbage-collected locally.

---

## Design: Garbage Collection

### Manual: `poof gc`

```
poof gc <project>                  # GC one project
poof gc --all                      # GC all projects

Flags:
  --keep N          Keep the N most recent images, delete the rest (default: 3)
  --older-than N    Delete images older than N days
  --dry-run         Show what would be deleted, don't delete
```

When both `--keep` and `--older-than` are given, an image must satisfy BOTH conditions to be deleted (i.e. it must be outside the keep window AND older than N days). This prevents accidentally deleting recent images.

### Automatic: `poof gc set`

```
poof gc set <project> --keep 5         # Set policy for one project
poof gc set --all --keep 3             # Set global default policy
poof gc set --all --older-than 14      # Alternative: age-based default

poof gc status                         # Show current policies per project
poof gc off <project>                  # Disable GC for one project
poof gc off --all                      # Disable GC globally
```

When a policy is set, GC runs automatically at the end of every deploy. A deploy of ANY project triggers GC for ALL projects that have a policy (or inherit the global default). This is simple and ensures cleanup happens regularly without needing a separate scheduler.

The default policy (when none is explicitly set) is `--keep 3`.

### Server-side implementation

**Storage:** Add a `gc_policies` table (or settings in the existing `settings` table):

```sql
-- Option A: dedicated table
CREATE TABLE gc_policies (
    project TEXT PRIMARY KEY,  -- project name, or "*" for global default
    keep_count INTEGER,        -- NULL if not set
    older_than_days INTEGER    -- NULL if not set
);
```

**API endpoints:**

```
POST   /gc                          -- trigger manual GC (body: {"project": "x"} or {"all": true}, optional overrides)
GET    /gc/status                   -- return policies for all projects
PUT    /gc/policy/{name}            -- set policy for a project (or "_default" for global)
DELETE /gc/policy/{name}            -- remove policy for a project
```

**GC logic** (in `docker/docker.go` or a new `docker/gc.go`):

```
func GC(project, image string, keep int, olderThanDays int) (removed []string, err error)
```

1. List all local Docker images matching the project's registry/repo pattern
   - `docker images --format '{{.Repository}}:{{.Tag}} {{.CreatedAt}}' | grep <repo>`
2. Sort by creation date (newest first)
3. Identify the currently running image (never delete it)
4. Apply keep/age filters to determine which to delete
5. For each image to delete: `docker rmi <image>`
6. Return list of removed images for logging

**Post-deploy hook** (in `server/handlers.go`, at the end of `runDeploy`):

After a successful deploy, trigger GC for all projects that have a policy. Run it in a goroutine so it doesn't block the deploy response. Log results.

```go
// At the end of runDeploy, after the success response:
go s.runGCAll()
```

**Also prune dangling images** after the per-project GC:

```
docker image prune -f
```

This catches `<none>:<none>` images that accumulate from tag overwrites (e.g. re-pulling `:latest`).

---

## Design: Enhanced Rollback

### Current behavior (unchanged)

```
poof rollback <project>    # Redeploy the previous successful image
```

### New: time-based rollback

```
poof rollback <project> --before 2026-04-20       # Last deploy before that date
poof rollback <project> --before today             # Last deploy before today
poof rollback <project> --before yesterday         # Last deploy before yesterday
```

**Flow for `--before`:**

1. Query SQLite: `SELECT image, deployed_at FROM deployments WHERE project = ? AND status = 'success' AND deployed_at < ? ORDER BY deployed_at DESC`
2. Walk through candidates in order (newest first):
   a. Is the image cached locally? (`docker inspect`) → use it, done
   b. Try `docker pull` from registry → success? → use it, done
   c. Image unavailable → log it, try the next candidate
3. If no candidate is available, report what was tried:
   ```
   No available image found before 2026-04-20. Tried:
     2026-04-19 15:56  6fca30...  not found locally or in registry
     2026-04-18 09:12  ab12cd...  not found locally or in registry
   ```

This "walk backwards" approach is resilient to images disappearing from both local disk (GC'd) and registry (manual cleanup, retention policies, repo visibility changes, etc.).

### New: deployment history

```
poof rollback <project> --list
```

Shows deployment history with availability status:

```
  #   Date                 Image         Status
  42  2026-04-26 20:32     0df529...     local (current)
  41  2026-04-26 15:27     20d886...     local
  40  2026-04-25 00:25     8f6c7b...     local
  39  2026-04-20 17:12     2ab4be...     remote
  38  2026-04-19 15:56     6fca30...     gone
```

**Status detection:**

- `local`: image exists on disk (`docker inspect` succeeds)
- `local (current)`: image is running in the active container
- `remote`: not local, but tag exists in registry (query GHCR OCI API or GitHub Packages API)
- `gone`: not found locally or in registry

**Registry check** (for `--list` and fallback pulls): Poof already stores the full image reference (e.g. `ghcr.io/racso/repo:sha`). To check remote availability:

1. Parse registry host from image reference (already implemented: `registryHost()` in `docker.go`)
2. Authenticate: `GET https://ghcr.io/token?scope=repository:owner/repo:pull` (using existing GitHub credentials)
3. Check tag: `HEAD https://ghcr.io/v2/owner/repo/manifests/<tag>` — 200 means it exists, 404 means gone

Note: the `--list` status check for remote images can be slow if there are many deployments. Consider caching or limiting the check to the most recent N entries (e.g. 20), marking older ones as `unknown` instead of making registry calls for each.

---

## Implementation order

1. **GC core** — `docker/gc.go`: image listing, filtering, deletion logic
2. **GC API** — server endpoints + policy storage + post-deploy trigger
3. **`poof gc` CLI** — manual GC + policy management commands
4. **Rollback `--list`** — deployment history with local availability check
5. **Rollback `--before`** — time-based candidate walking with pull fallback
6. **Registry availability check** — add remote status to `--list` and `--before` fallback

Steps 1-3 (GC) and 4-6 (rollback) are independent and can be developed in parallel.

---

## Notes

- The default `--keep 3` is deliberately aggressive. The registry serves as the deep archive; local images are a cache for fast rollback. If a project needs more local copies (e.g. registry is unreliable), override with `poof gc set <project> --keep 10`.
- `docker rmi` may fail if an image is used by a stopped container. GC should handle this gracefully (log and skip).
- Build cache (`docker builder prune`) and dangling images (`docker image prune`) are separate from per-project GC but should be part of the automatic cleanup cycle.
- Date parsing for `--before` should accept ISO dates (`2026-04-20`), `today`, and `yesterday`. Fancier relative dates (e.g. "3 days ago") are nice-to-have but not required for v1.
