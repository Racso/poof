# Garbage Collection & Enhanced Rollback

Design document for image garbage collection (`poof gc`), time-based rollback (`poof rollback --before`), and the underlying schema changes needed to make both robust.

---

## Problem

Every deploy pulls a new Docker image. Old images are never deleted. After enough deploys, disk fills up with hundreds of unused images (we observed 342 images / 41 GB on a single server with 27 running containers).

Static deploys have a separate, hardcoded cleanup (`pruneVersions` in `static/static.go`, keeps 5 versions). This should be unified with the GC system so all artifact retention is configured in one place.

Additionally, several project lifecycle events cause images to become **orphaned** — present on disk but invisible to any cleanup logic:

- **Project converted to static**: old container images stay on disk, but the project no longer uses Docker.
- **Project renamed**: images under the old name are abandoned.
- **Project deleted**: all associated images are left behind.

Meanwhile, rollback only supports "go back one version." There's no way to roll back to a specific point in time, and no resilience if the target image has been garbage-collected locally.

---

## Schema Changes

### Project IDs

The `projects` table currently uses `name` as the primary key. The `deployments` table references projects by name with `ON DELETE CASCADE`.

**Change:** Add an autoincrement `id` to `projects`. Reference `project_id` in `deployments` instead of `project` (name). Remove `ON DELETE CASCADE`.

```sql
CREATE TABLE projects (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    name    TEXT UNIQUE NOT NULL,
    ...
);

CREATE TABLE deployments (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL,
    image       TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'success',
    deployed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (project_id) REFERENCES projects(id)  -- no CASCADE
);
```

**Why:** Without cascade, deployment history survives project deletion. The ID ensures that if a project is deleted and a new one is created with the same name, the old deployment records don't silently attach to the new project — they point to a different ID.

**API and CLI are unchanged** — everything external still uses project names. The ID is purely internal, used only for DB foreign keys.

### Image Labels (optional, informational only)

Poof can add OCI labels to images built via CI (`workflowTemplate` in `github/github.go`):

```yaml
docker build -t $IMAGE \
  --label io.poof.managed=true \
  --label io.poof.project=POOF_PROJECT_NAME \
  POOF_BUILD_ARGS
```

These are **purely informational** — useful for `docker inspect` debugging ("where did this image come from?") but not used by any automated logic. Labels cannot be added to images pulled from external sources without creating a duplicate layer, so they can't serve as a universal ownership marker.

**All GC ownership is determined by the `deployments` table** (see below). This is the single source of truth for "Poof manages this image."

---

## Design: Garbage Collection

### Mark-and-Sweep Model

GC operates on a clear ownership boundary: **only touch images that Poof deployed.**

An image is considered Poof-managed if its reference appears in the `deployments` table. This is the single source of truth — it covers images built by Poof CI, images pulled from external registries, and images from projects that have since been deleted, renamed, or converted to static.

Images not in the deployments table are **never touched** — they belong to something else (base images like mysql/nginx/wordpress, manually pulled images, locally built images, etc.).

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

`poof gc --all` also handles orphaned images — images that are Poof-managed (labeled or in deployment history) but whose project no longer exists or has been converted to static. These are always eligible for deletion since no running container uses them.

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

**Storage:** Add a `gc_policies` table:

```sql
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

**GC logic** (in `docker/gc.go`):

```
func GC(project, image string, keep int, olderThanDays int) (removed []string, err error)
```

1. List all local Docker images matching the project's registry/repo pattern
2. Sort by creation date (newest first)
3. Identify the currently running image (never delete it)
4. Apply keep/age filters to determine which to delete
5. For each image to delete: `docker rmi <image>`
6. Return list of removed images for logging

For orphan sweep (on `--all`):

1. List all image references from the `deployments` table = all Poof-managed images
2. Match against images actually present on disk (`docker images`)
3. Subtract images used by running containers
4. Subtract images within any active project's keep-N policy
5. Delete the rest (includes images from deleted/renamed/static-converted projects)

**Post-deploy hook** (in `server/handlers.go`, at the end of `runDeploy`):

After a successful deploy, trigger GC for all projects. Run in a goroutine so it doesn't block the deploy response. Log results.

```go
go s.runGCAll()
```

**Also prune dangling images** after the per-project GC:

```
docker image prune -f
```

This catches `<none>:<none>` images that accumulate from tag overwrites (e.g. re-pulling `:latest`).

**Static project cleanup:** GC also applies to static projects. Currently, the tarball is streamed and discarded — only the extracted directory is kept. This should change: tarballs are the static equivalent of Docker images (the stored artifact), and extracted directories are the equivalent of running containers (the runtime). The new flow:

1. Save tarball to `versions/v<depID>.tar.gz`
2. Extract to `versions/v<depID>/`
3. Swap `current` symlink to the extracted directory
4. GC prunes old **tarballs** per the `--keep N` policy (same as Docker images)
5. GC prunes old **extracted directories** aggressively (only keep current; re-extract from tarball on rollback)

This enables `--before` rollback for static sites: walk the deployment history, find the tarball, re-extract, swap symlink — same pattern as Docker rollback (find image, re-run container). If the tarball has been GC'd, try the next candidate, same walk-backwards logic.

This replaces the current hardcoded `maxVersions = 5` in `static/static.go` — all artifact retention (Docker images and static tarballs) is configured through the same `poof gc set` mechanism.

**Deployment history cleanup:** During GC, also delete deployment records that reference images no longer present on disk or in the registry, and whose project no longer exists. This prevents the deployments table from growing unbounded with stale records from long-deleted projects.

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

1. Query SQLite: `SELECT image, deployed_at FROM deployments WHERE project_id = ? AND status = 'success' AND deployed_at < ? ORDER BY deployed_at DESC`
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

## Implementation Order

1. **Schema migration** — Add project IDs, update deployments FK, remove cascade, migrate existing data
2. **Image labels** (optional) — Update CI workflow template for informational labels
3. **GC core** — `docker/gc.go`: mark-and-sweep logic using deployment history as ownership source
4. **GC API** — server endpoints + policy storage + post-deploy trigger
5. **`poof gc` CLI** — manual GC + policy management commands
6. **Rollback `--list`** — deployment history with local availability check
7. **Rollback `--before`** — time-based candidate walking with pull fallback
8. **Registry availability check** — add remote status to `--list` and `--before` fallback

Steps 1-2 are prerequisites. Steps 3-5 (GC) and 6-8 (rollback) are independent and can be developed in parallel after that.

---

## Notes

- The default `--keep 3` is deliberately aggressive. The registry serves as the deep archive; local images are a cache for fast rollback. If a project needs more local copies (e.g. registry is unreliable), override with `poof gc set <project> --keep 10`.
- `docker rmi` may fail if an image is used by a stopped container. GC should handle this gracefully (log and skip).
- Build cache (`docker builder prune`) and dangling images (`docker image prune`) are separate from per-project GC but should be part of the automatic cleanup cycle.
- Date parsing for `--before` should accept ISO dates (`2026-04-20`), `today`, and `yesterday`. Fancier relative dates (e.g. "3 days ago") are nice-to-have but not required for v1.
- CI labels (`io.poof.managed`, `io.poof.project`) are optional and purely informational — useful for `docker inspect` debugging, not used by any automated logic.
- Deployment history cleanup (deleting stale records from deleted projects) should be conservative — only remove records for images confirmed gone from both disk and registry.
