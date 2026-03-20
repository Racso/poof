# Poof — Plan

Lightweight self-hosted deployment daemon. Single binary. API-only.
Push to git → image built in CI → Poof deploys it. Zero friction.

Named after the magic: configure once, and *poof* — it's live.

---

## What Poof is NOT

- Not a build system. Dockerfiles are required. That's a feature: every project is explicit about its runtime.
- Not a DNS manager. A wildcard DNS record (`*.domain → server`) handles routing. No per-project DNS changes ever needed.
- Not a UI. REST API + CLI only. Easier to administer, scriptable, and keeps the binary lean.
- Not a database provisioner.
- Not multi-server.

---

## Architecture

### Single binary, two modes

```
poof server          # runs as daemon on the server
poof <command>       # CLI client, talks to server API
```

Same binary. The server exposes a REST API; the CLI is a thin wrapper around it.

### State

SQLite (single file, e.g. `/var/lib/poof/poof.db`). Stores projects and deployment history. Zero ops overhead, trivially backed up.

### Routing & TLS

Caddy Docker Proxy (`lucaslorentz/caddy-docker-proxy`) already runs on the server. When Poof starts a container, it sets the right Caddy labels. Caddy picks it up automatically and gets a TLS cert via ACME HTTP-01. Poof does not touch Caddy's config directly.

### DNS

A single wildcard A record (`*.domain → Droplet IP`) is provisioned once via Terraform. Poof never touches DNS. Any subdomain automatically reaches the server; Caddy only responds to subdomains with a running container.

---

## Deploy flow

```
git push
  → GitHub Action: docker build → docker push to GHCR
  → GitHub Action: POST /hooks/github/:project  (with image tag)
  → Poof: docker pull image
  → Poof: run container with Caddy labels
  → Caddy: picks up new container, issues TLS cert
  → live at project.domain
```

Builds happen in free GitHub Actions compute. The server is a runtime only, never a build machine.

---

## Configuration

Poof has a server-side config file (e.g. `/etc/poof/poof.toml`):

```toml
domain   = "rac.so"           # default domain for subdomain inference
api_port = 9000
data_dir = "/var/lib/poof"

[github]
user = "racso"                # used to infer default image names

[auth]
token = "..."                 # bearer token required for all API calls
```

Per-project config is stored in SQLite and managed via CLI/API. No config files per project in this repo.

---

## CLI — smart defaults

The goal: `poof add <name>` should work with zero extra flags for the common case.

```
poof add demo
```

Infers:
- **domain**: `demo.rac.so` (name + configured root domain)
- **image**: `ghcr.io/racso/demo` (configured GitHub user + name)
- **branch**: `main`
- **port**: `8080`

Override anything explicitly:

```
poof add demo --domain api.rac.so --image ghcr.io/other/image --port 3000
```

### Full CLI surface

```
poof add <name> [flags]        register a new project
poof remove <name>             remove project and stop its container
poof list                      list all projects and their status
poof status <name>             project details + current deployment
poof deploy <name>             trigger manual redeploy (latest image)
poof rollback <name>           redeploy the previous image
poof logs <name> [--lines N]   last N log lines from the container
poof env get <name>            list env var keys (values hidden)
poof env set <name> KEY=VALUE  set one or more env vars
poof env unset <name> KEY      remove an env var
poof server                    start the daemon
```

---

## API

All endpoints require `Authorization: Bearer <token>`.

```
POST   /hooks/github/:project     webhook (called by GitHub Action)
GET    /projects                  list projects
POST   /projects                  register project
GET    /projects/:name            project details + last deployment
DELETE /projects/:name            remove project
POST   /projects/:name/deploy     trigger manual deploy
POST   /projects/:name/rollback   redeploy previous image
GET    /projects/:name/logs       last N log lines
GET    /projects/:name/env        list env var keys
PUT    /projects/:name/env        set env vars
DELETE /projects/:name/env/:key   remove env var
```

---

## GitHub Action (project-side template)

Each project repo includes this action. Copy-paste once, never touch again.

```yaml
# .github/workflows/deploy.yml
on:
  push:
    branches: [main]

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Build and push image
        run: |
          echo "${{ secrets.GITHUB_TOKEN }}" | docker login ghcr.io -u $ --password-stdin
          IMAGE=ghcr.io/${{ github.repository }}:${{ github.sha }}
          docker build -t $IMAGE .
          docker push $IMAGE

      - name: Notify Poof
        run: |
          curl -fsSL -X POST ${{ secrets.POOF_URL }}/hooks/github/${{ github.event.repository.name }} \
            -H "Authorization: Bearer ${{ secrets.POOF_TOKEN }}" \
            -H "Content-Type: application/json" \
            -d '{"image": "ghcr.io/${{ github.repository }}:${{ github.sha }}"}'
```

Two secrets per repo: `POOF_URL` and `POOF_TOKEN`. These can be set as GitHub org secrets so you only configure them once across all repos.

---

## Project structure (Go)

```
poof/
  main.go               entry point, CLI dispatch
  cmd/
    server.go           daemon startup
    add.go              poof add
    deploy.go           poof deploy
    ...
  server/
    server.go           HTTP server setup
    hooks.go            webhook handler
    projects.go         project CRUD handlers
    logs.go             log streaming handler
  docker/
    client.go           Docker daemon interaction (pull, run, stop, logs)
  store/
    store.go            SQLite access (projects, deployments)
  config/
    config.go           TOML config loading
```

---

## Installation (target experience)

```bash
curl -fsSL https://raw.githubusercontent.com/racso/poof/main/install.sh | sh
poof server &
```

The install script downloads the appropriate binary for the platform from GitHub Releases.

---

## Non-obvious decisions

**Why CI builds, not server builds?**
The server stays a runtime. Build failures happen in CI with full logs, not on your production server. Game servers and anything CPU-heavy won't spike the server at deploy time. Also: free GitHub Actions minutes.

**Why SQLite and not a flat file?**
Deployment history (rollbacks, timestamps, image tags) benefits from queryable structure. SQLite has zero operational overhead and the file is a single backup target.

**Why Caddy Docker Proxy and not direct Caddy config?**
Caddy Docker Proxy watches the Docker socket and reconfigures Caddy from container labels. Poof never needs to write or reload Caddy config — starting a container is enough.

**Why wildcard DNS?**
Eliminates DNS from the deploy path entirely. No API calls, no propagation delays, no Cloudflare token needed by Poof. A specific record always overrides the wildcard, so individual subdomains can still point elsewhere.
