# Poof! — Plan

Lightweight self-hosted deployment daemon. Single binary. API-only.
Push to git → image built in CI → Poof! deploys it. Zero friction.

Named after the magic: configure once, and *poof!* — it's live.

---

## What Poof! is NOT

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

SQLite (single file, e.g. `/var/lib/poof/poof.db`). Stores projects, deployment history, and per-project secrets. Zero ops overhead, trivially backed up.

### Routing & TLS

Caddy Docker Proxy (`lucaslorentz/caddy-docker-proxy`) already runs on the server. When Poof! starts a container, it sets the right Caddy labels. Caddy picks it up automatically and gets a TLS cert via ACME HTTP-01. Poof! does not touch Caddy's config directly.

### DNS

A single wildcard A record (`*.domain → Droplet IP`) is provisioned once via Terraform. Poof! never touches DNS. Any subdomain automatically reaches the server; Caddy only responds to subdomains with a running container.

---

## Deploy flow

```
git push
  → GitHub Action: docker build → docker push to GHCR
  → GitHub Action: POST /hooks/github/:project  (with image tag + per-project token)
  → Poof!: validate token → docker pull image
  → Poof!: run container with Caddy labels
  → Caddy: picks up new container, issues TLS cert
  → live at project.domain
```

Builds happen in free GitHub Actions compute. The server is a runtime only, never a build machine.

---

## Configuration

Poof! has a server-side config file (`/etc/poof/poof.toml`):

```toml
domain   = "rac.so"           # default domain for subdomain inference
api_port = 9000
data_dir = "/var/lib/poof"

[github]
user  = "racso"               # used to infer default image and repo names
token = "ghp_..."             # PAT with repo scope — used to automate project setup

[auth]
token = "..."                 # bearer token to authenticate CLI → server API calls
```

Per-project config is stored in SQLite and managed via CLI/API. No config files per project in this repo.

---

## GitHub integration — fully automated project setup

When a GitHub PAT is configured, `poof add` does everything automatically via the GitHub API:

```
poof add demo
  → generate per-project secret (stored in SQLite)
  → POST /repos/racso/demo/hooks
      creates webhook pointing at https://server/hooks/github/demo
  → GET  /repos/racso/demo/actions/secrets/public-key
  → PUT  /repos/racso/demo/actions/secrets/POOF_TOKEN
      encrypts per-project secret with repo's NaCl public key, stores as secret
  → PUT  /repos/racso/demo/contents/.github/workflows/poof.yml
      commits the GitHub Action workflow directly into the repo
  → done. next git push deploys automatically.
```

The developer never manually sets secrets or copies workflow files.

### Authentication: per-project tokens

Each project gets its own randomly generated token, stored in SQLite. The GitHub Action uses this token (`POOF_TOKEN`) to authenticate its webhook call to Poof!. Compromising one project's token does not affect others.

`POOF_URL` (the server's public address) is also set as a repo secret by `poof add`.

### The GitHub Action (committed automatically)

Poof! commits this workflow into `.github/workflows/poof.yml` in the project repo:

```yaml
on:
  push:
    branches: [main]

permissions:
  contents: read
  packages: write

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Build and push image
        run: |
          echo "${{ secrets.GITHUB_TOKEN }}" | docker login ghcr.io -u ${{ github.actor }} --password-stdin
          IMAGE=ghcr.io/${{ github.repository }}:${{ github.sha }}
          docker build -t $IMAGE .
          docker push $IMAGE

      - name: Notify Poof!
        run: |
          curl -fsSL -X POST ${{ secrets.POOF_URL }}/hooks/github/${{ github.event.repository.name }} \
            -H "Authorization: Bearer ${{ secrets.POOF_TOKEN }}" \
            -H "Content-Type: application/json" \
            -d '{"image": "ghcr.io/${{ github.repository }}:${{ github.sha }}"}'
```

`GITHUB_TOKEN` is automatically provided by GitHub Actions for every run (no setup). With `packages: write` it can push to GHCR for that repo at no cost.

---

## CLI — smart defaults

The goal: `poof add <name>` should work with zero extra flags for the common case.

```
poof add demo
```

Infers:
- **domain**: `demo.rac.so` (name + configured root domain)
- **image**: `ghcr.io/racso/demo` (configured GitHub user + name)
- **repo**: `racso/demo` (configured GitHub user + name)
- **branch**: `main`
- **port**: `8080`

Override anything explicitly:

```
poof add demo --domain api.rac.so --image ghcr.io/other/image --port 3000 --repo racso/other-repo
```

### Full CLI surface

```
poof add <name> [flags]        register project + automate GitHub setup
poof remove <name>             remove project, stop container, remove webhook
poof list                      list all projects and their status
poof status <name>             project details + current deployment
poof deploy <name>             trigger manual redeploy (latest image)
poof rollback <name>           redeploy the previous image
poof logs <name> [--lines N]   last N log lines from the container
poof env get <name>            list env var keys (values hidden)
poof env set <name> KEY=VALUE  set one or more env vars (triggers redeploy)
poof env unset <name> KEY      remove an env var (triggers redeploy)
poof server                    start the daemon
```

---

## API

All endpoints require `Authorization: Bearer <token>` (the global API token from config).
Webhook endpoint authenticates via per-project token instead.

```
POST   /hooks/github/:project     webhook (called by GitHub Action, per-project token)
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

## Project structure (Go)

```
poof/
  main.go               entry point, CLI dispatch
  cmd/
    server.go           daemon startup
    add.go              poof add
    remove.go           poof remove
    deploy.go           poof deploy/rollback
    logs.go             poof logs
    env.go              poof env get/set/unset
    list.go             poof list/status
  server/
    server.go           HTTP server setup
    hooks.go            webhook handler
    projects.go         project CRUD handlers
    logs.go             log streaming handler
  docker/
    client.go           Docker daemon interaction (pull, run, stop, logs)
  github/
    client.go           GitHub API: webhooks, secrets, workflow file commit
  store/
    store.go            SQLite access (projects, deployments, secrets)
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
The server stays a runtime. Build failures happen in CI with full logs, not on the production server. Game servers and anything CPU-heavy won't spike the server at deploy time. Free GitHub Actions minutes do the work.

**Why SQLite and not a flat file?**
Deployment history (rollbacks, timestamps, image tags) benefits from queryable structure. SQLite has zero operational overhead and the file is a single backup target.

**Why Caddy Docker Proxy and not direct Caddy config?**
Caddy Docker Proxy watches the Docker socket and reconfigures Caddy from container labels. Poof! never needs to write or reload Caddy config — starting a container is enough.

**Why wildcard DNS?**
Eliminates DNS from the deploy path entirely. No API calls, no propagation delays, no Cloudflare token needed by Poof!. A specific record always overrides the wildcard, so individual subdomains can still point elsewhere.

**Why per-project tokens instead of a global webhook token?**
Isolation. The GitHub Action for each project holds only its own token. Rotating or revoking one project's access doesn't affect others. Tokens are generated by Poof! and set as repo secrets automatically — the developer never sees them.

**Why commit the GH Action via API instead of a template?**
Zero manual steps for the developer. `poof add` is the only thing that needs to happen. The committed workflow is also Poof!-version-aware: if the webhook format changes, `poof add` (or a future `poof upgrade`) can update it.
