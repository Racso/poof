# Poof!

Lightweight self-hosted deployment daemon. Single Go binary that acts as both CLI and server. Workflow: `poof add myapp` → `git push` → live at `myapp.yourdomain.com`. Caddy fronts everything for automatic TLS via a wildcard DNS record.

## What Poof! is

A daemon that runs on one Linux server with Docker, plus a CLI that talks to it. The server registers projects, owns a SQLite store, runs containers on the `poof-net` Docker network, and pushes routing config to a Caddy container's admin API. GitHub Actions builds and pushes images to GHCR, then calls the server's deploy endpoint with the new image tag. Zero per-project DNS work — one wildcard A record covers everything.

## What Poof! is NOT

- Not a Kubernetes / Nomad replacement (single host, no orchestration).
- Not a build system (CI builds the image; Poof! only deploys it).
- Not a multi-tenant PaaS (one operator, one server).
- Not a secret manager (env vars are stored, but not encrypted-at-rest beyond filesystem perms).

## Architecture

```
main.go → cmd.Execute()       (cobra root)
cmd/                          CLI subcommands (one file per command)
server/                       HTTP daemon: handlers.go, server.go, gc.go, update.go
store/                        SQLite-backed persistence (modernc.org/sqlite)
config/                       Client + server config TOML loader
caddy/                        Generates Caddyfile, talks to Caddy admin API
docker/                       Docker engine wrapper (deploy, stop, logs, gc)
github/                       GitHub API client: secrets + workflow file commits
static/                       Static-site serving support (mode + GC of unused dirs)
defaults/                     Compiled-in defaults (workflow templates, etc.)
landing/                      Static landing page served at poof.rac.so
scripts/migrate-url.sh        One-shot migration helpers
install.sh                    Bootstraps server or client install
Dockerfile                    Builds the server image used by `poof install`
```

Server entrypoints live in `cmd/server.go` and `cmd/install.go`. CLI entrypoints share `cmd/root.go` (cobra root, profile flags, HTTP client to the server).

## Data model (`store/store.go`)

- **Project** — name, domain, image, repo, branch, port, subpath mode, folder (monorepo), static mode (`"" | static | spa`), build flag, CI flag, CI mode (`managed` | `callable`).
- **Volume** — managed (`/var/lib/poof/<project>/<container-path>`) or explicit (`host:container`).
- **Network** — Poof-managed Docker network (`name`, `internal`) plus a `project_networks` join table of per-project attachments. Re-applied on every (re)deploy.
- **Redirect** — independent 301 from one domain to another.
- **Deployment** — image tag, status, timestamp; powers rollback.
- **GC policy** — per-project + global default (`keep`, `older_than_days`, `disabled`).

## CLI surface (`poof --help`)

Project lifecycle:
- `poof add <name>` — register; auto-sets GitHub secrets + commits `.github/workflows/poof.yml` if a PAT is configured.
- `poof configure <name>` — change any field except token; only passed flags mutate.
- `poof clone <src> <suffix>` — create `<src>-<suffix>` deploying from branch `<suffix>`; optional `--env --all|--only|--except|--ask`. Refuses if the source has a Caddy snippet unless `--caddy-yes` (copy verbatim — references to the source's container are NOT rewritten) or `--caddy-no` (skip) is passed.
- `poof remove <name>` — stop container, delete project; `--data-keep|--data-delete` for managed volumes.
- `poof refresh <name>` — re-sync GitHub secrets + workflow file (idempotent).
- `poof apply [-f poof.ini] [--dry-run] [--prune]` — declarative INI sync.

Deploy / observe:
- `poof deploy <name> [--image <tag>]` — manual redeploy.
- `poof rollback <name>` — redeploy previous successful image.
- `poof status <name>`, `poof list`, `poof logs <name> [-n N]`, `poof server-logs`, `poof troubleshoot`, `poof version`.

Config / env / volumes / redirects:
- `poof config [set <key> [value]]` — local keys: `server`, `token`. Server keys: `domain`, `github-user`, `github-token`. With `--profile` / `--profile-env` for multi-server setups.
- `poof env get|set|unset|copy` — copy supports `--all|--only|--except|--ask`.
- `poof volume add|list|remove` — managed (`/app/data`) or explicit (`/host:/container`).
- `poof net create|ls|delete|add|list|remove` — Poof-managed Docker networks for private inter-project traffic. `create <name> [--internal]` defines a network (DB-backed desired state); `add <project> <network>` attaches it. Poof re-attaches on every (re)deploy alongside the mandatory poof-net, so membership survives redeploys (unlike a one-off `docker network connect`). `delete` refuses while any project is still attached and leaves the underlying Docker network in place.
- `poof redirect add|list|delete` — domain-level 301s independent of any project.

Caddy / GC / install / update:
- `poof caddy get|set|delete|list` — per-project Caddy snippet override (in addition to `/etc/caddy/conf.d/*.Caddyfile` static files).
- `poof gc [project] [--keep N] [--older-than D] [--all] [--dry-run]`, `poof gc set|status|off`.
- `poof install [--domain --token --use-caddy --yes]` — bootstraps Caddy + server container.
- `poof update local|server|both [version]`.
- `poof migrate workflows [--apply]` — one-shot migrations across breaking releases.

Spells (`poof spell <name>`):
- `poof spell proxy <source> <target>` — install a Caddy reverse_proxy on a source project. Source: `<project>` or `<project>/<path>`. Target: `<project>` (port resolved from DB) or `<container>:<port>`. Strip-prefix implicit when path is present; `--keep-prefix` opts out.
- `poof spell clean-urls <project>` — install `try_files {path}.html {path}` so static sites serve `/about` from `/about.html`.
- `poof spell to-static <project> [--spa] [--build]` — reconfigure a container-served project as `--static`. Doesn't redeploy (static deploys need local files); prints the next command.

Snippet-writing spells (`proxy`, `clean-urls`) refuse to overwrite an existing Caddy snippet — clear it with `poof caddy delete <name>` first. `to-static` refuses if the project is already static. Reversal for snippet spells is `poof caddy delete <project>`; for `to-static` it's `poof configure --no-static`.

Global flags: `--profile <name>`, `--profile-env`.

## Design principle: flags vs spells

Core commands (`add`, `configure`, `clone`, ...) only grow flags when the behavior can't be decomposed into a follow-up command, or when decomposing it would be too expensive (e.g. forces an extra deploy). Everything else lives under `poof spell <name>` as a named recipe. This keeps the per-command flag surface small and the catalog of small composite actions discoverable in one place (`poof spell --help`).

## Project options reference

| Option | Default | Notes |
|---|---|---|
| `--domain` | `<name>.<root-domain>` | Custom domain otherwise. |
| `--image` | `ghcr.io/<github-user>/<name>` | |
| `--repo` | `<github-user>/<name>` | |
| `--branch` | `main` | |
| `--port` | `80` | Container port. |
| `--folder` | (none) | Monorepo subdir; workflow triggers + builds scoped to it. |
| `--static` | off | Serve files via Caddy instead of running a container. |
| `--spa` | off | Adds `try_files` fallback to `index.html` (requires `--static`). |
| `--build` | off | Build static assets via Dockerfile, output to `/poof` (requires `--static`). |
| `--subpath` | `disabled` | `disabled | redirect | proxy`; default settable in `poof.toml`. |
| `--ci` | `yes` | `yes` (push-triggered), `no`, or `callable` (reusable workflow). |

## Server config (`/etc/poof/poof.toml`)

```toml
token            = "..."                              # required
public_url       = "https://poof.yourdomain.com"      # routed by Caddy to the daemon
api_port         = 9000
data_dir         = "/var/lib/poof"
caddy_admin_url  = "http://caddy-proxy:2019"
caddy_static_dir = "/etc/caddy/conf.d"
subpath_default  = "disabled"                          # disabled | redirect | proxy
```

The server also stores GitHub credentials (`github_user`, `github_token`) and the public domain — set via `poof config set` from the client.

## Client config (`~/.config/poof/poof.toml`)

```toml
server = "https://poof.yourdomain.com"
token  = "..."

[work]                       # named profile
server = "https://poof.work.com"
token  = "..."
# import = "~/.config/poof/work.toml"  # alternative: pull profile from another file
```

Selected via `--profile work` or `POOF_PROFILE=work` + `--profile-env`.

## Routing model

- Default: project lives at its subdomain (`<name>.<root-domain>`).
- Subpath `redirect`: `<root>/<name>/*` → 301 → `<name>.<root>/*`.
- Subpath `proxy`: `<root>/<name>/*` transparently proxied to the container (app must handle subpath).
- Manual Caddyfiles: drop `*.Caddyfile` into `caddy_static_dir` for non-Poof services; survives reloads.
- Per-project snippet override: `poof caddy set <name>` pushes a snippet that takes precedence over the generated route.
- `poof redirect` rules apply at the Caddy layer, independent of any project.

## CI integration

When a GitHub PAT is configured, `poof add` / `poof refresh`:
1. Sets repo secrets `POOF_URL` and `POOF_TOKEN`.
2. Commits `.github/workflows/poof.yml` (template in `defaults/`) **to the project's configured branch** — push triggers only fire for workflow files present on the pushed branch, so committing to the default branch would leave non-default-branch projects (e.g. clones) without CI. If the branch doesn't exist on GitHub yet, the commit fails with a hint to push the branch and run `poof refresh`.
3. The workflow builds + pushes to GHCR on `push` to the configured branch (and only on changes to `--folder` if set), then `POST`s to the server's deploy endpoint.

CI modes: `managed` (default; standalone push-triggered workflow) or `callable` (`on: workflow_call` reusable workflow invoked from a user-owned outer workflow that wraps tests/lint/matrix builds).

## Garbage collection

Detailed design lives in `GC_ROLL.md`. Per-project policy (`keep`, `older_than_days`) plus a global default; both conditions AND together when both are set. Sweeps orphan images and prunes dangling layers on schedule. `--dry-run` shows planned deletions.

## Pending ideas (not yet implemented)

Captured here so they aren't lost. Add new ones to this section as they come up.

### `poof rollback --before <date>` — time-based rollback

Roll back to the last successful deploy before a given date, walking history with pull fallback.

```
poof rollback <project> --before 2026-04-20
poof rollback <project> --before today
poof rollback <project> --before yesterday
```

Flow: query `deployments` for `status='success' AND deployed_at < ?` newest-first, then for each candidate (a) `docker inspect` locally, (b) `docker pull` from registry, (c) skip and try the next. If none available, report what was tried. Resilient to images disappearing from both local disk (GC'd) and the registry.

Date parsing: ISO (`2026-04-20`), `today`, `yesterday`. Relative phrases ("3 days ago") are nice-to-have.

### `poof rollback --list` — deployment history with availability

```
  #   Date                 Image         Status
  42  2026-04-26 20:32     0df529...     local (current)
  41  2026-04-26 15:27     20d886...     local
  40  2026-04-25 00:25     8f6c7b...     local
  39  2026-04-20 17:12     2ab4be...     remote
  38  2026-04-19 15:56     6fca30...     gone
```

Status detection:
- `local` — `docker inspect` succeeds.
- `local (current)` — image is the running container's.
- `remote` — `HEAD https://<registry>/v2/<owner>/<repo>/manifests/<tag>` returns 200 (auth via existing GitHub creds; `registryHost()` already exists in `docker.go`).
- `gone` — neither local nor remote.

Registry checks can be slow with many entries; consider limiting to the most recent ~20 and marking older as `unknown`.

## Existing docs in repo

- `README.md` — public-facing user docs.
- `CLAUDE.md` — this file; main source of truth for developers.
- `landing/index.html` — marketing page deployed at `poof.rac.so`.
