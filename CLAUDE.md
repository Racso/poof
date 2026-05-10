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
- **Redirect** — independent 301 from one domain to another.
- **Deployment** — image tag, status, timestamp; powers rollback.
- **GC policy** — per-project + global default (`keep`, `older_than_days`, `disabled`).

## CLI surface (`poof --help`)

Project lifecycle:
- `poof add <name>` — register; auto-sets GitHub secrets + commits `.github/workflows/poof.yml` if a PAT is configured.
- `poof configure <name>` — change any field except token; only passed flags mutate.
- `poof clone <src> <suffix>` — create `<src>-<suffix>` deploying from branch `<suffix>`; optional `--env --all|--only|--except|--ask`.
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
- `poof redirect add|list|delete` — domain-level 301s independent of any project.

Caddy / GC / install / update:
- `poof caddy get|set|delete|list` — per-project Caddy snippet override (in addition to `/etc/caddy/conf.d/*.Caddyfile` static files).
- `poof gc [project] [--keep N] [--older-than D] [--all] [--dry-run]`, `poof gc set|status|off`.
- `poof install [--domain --token --use-caddy --yes]` — bootstraps Caddy + server container.
- `poof update local|server|both [version]`.
- `poof migrate workflows [--apply]` — one-shot migrations across breaking releases.

Global flags: `--profile <name>`, `--profile-env`.

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
2. Commits `.github/workflows/poof.yml` (template in `defaults/`).
3. The workflow builds + pushes to GHCR on `push` to the configured branch (and only on changes to `--folder` if set), then `POST`s to the server's deploy endpoint.

CI modes: `managed` (default; standalone push-triggered workflow) or `callable` (`on: workflow_call` reusable workflow invoked from a user-owned outer workflow that wraps tests/lint/matrix builds).

## Garbage collection

Detailed design lives in `GC_ROLL.md`. Per-project policy (`keep`, `older_than_days`) plus a global default; both conditions AND together when both are set. Sweeps orphan images and prunes dangling layers on schedule. `--dry-run` shows planned deletions.

## Existing docs in repo

- `README.md` — public-facing user docs.
- `CLAUDE.md` — this file (project overview for future Claude sessions).
- `PLAN.md` — early design notes; some content overlaps with README.
- `GC_ROLL.md` — design doc for GC + time-based rollback schema.
- `landing/index.html` — marketing page deployed at `poof.rac.so`.
