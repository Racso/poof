# Poof!

Lightweight self-hosted deployment daemon. Single binary. Push to git → deployed.

```
poof add myapp
# next git push → live at myapp.yourdomain.com
```

## How it works

1. `poof add myapp` registers a project. If a GitHub PAT is configured, Poof! also sets `POOF_TOKEN` as a repo secret and commits the deploy workflow into the repo.
2. On every push to `main`, GitHub Actions builds a Docker image, pushes it to GHCR, then calls `POST /projects/myapp/deploy` on your Poof! server.
3. Poof! pulls the image, starts the container with Caddy labels, and Caddy handles TLS automatically.

No DNS changes needed — a single wildcard A record (`*.yourdomain.com → server`) covers everything.

## Requirements

- A Linux server with Docker
- [Caddy Docker Proxy](https://github.com/lucaslorentz/caddy-docker-proxy) running on a `caddy-net` Docker network
- A wildcard DNS A record pointing to the server
- A `Dockerfile` in each project repo

## Installation

```sh
curl -fsSL https://raw.githubusercontent.com/racso/poof/main/install.sh | sh
```

Or download a binary directly from [releases](https://github.com/racso/poof/releases).

## Configuration

Create `/etc/poof/poof.toml`:

```toml
domain     = "yourdomain.com"
api_port   = 9000
data_dir   = "/var/lib/poof"
public_url = "https://poof.yourdomain.com"  # set as POOF_URL repo secret

[github]
user  = "your-github-username"
token = "ghp_..."               # PAT with scopes: repo, workflow, read:packages, delete:packages

[auth]
token = "your-secret-token"     # used by CLI to authenticate with the server

[client]
server = "http://localhost:9000"
token  = "your-secret-token"
```

## Running

### As a Docker container (recommended)

```yaml
# docker-compose.yml
services:
  poof:
    image: ghcr.io/racso/poof:latest
    container_name: poof
    restart: always
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /var/lib/poof:/var/lib/poof
      - /etc/poof:/etc/poof:ro
    environment:
      - POOF_CONFIG=/etc/poof/poof.toml
    networks:
      - caddy-net
    labels:
      caddy: poof.yourdomain.com
      caddy.reverse_proxy: "{{upstreams 9000}}"

networks:
  caddy-net:
    external: true
```

### As a systemd service

```ini
[Unit]
Description=Poof! deployment daemon
After=network.target docker.service

[Service]
ExecStart=/usr/local/bin/poof server
Restart=always
Environment=POOF_CONFIG=/etc/poof/poof.toml

[Install]
WantedBy=multi-user.target
```

## CLI

```
poof add <name> [flags]        register project + automate GitHub setup
poof remove <name>             remove project, stop container
poof list                      list all projects and status
poof status <name>             project details + last deployment
poof deploy <name>             trigger manual redeploy
poof rollback <name>           redeploy previous image
poof logs <name> [--lines N]   container log lines
poof env get <name>            list env var keys (values never shown)
poof env set <name> KEY=VALUE  set env vars
poof env unset <name> KEY      remove env var
```

All flags have smart defaults — `poof add myapp` is usually enough.

## What Poof! is not

- Not a build system — Dockerfiles are required (by design)
- Not a DNS manager — use a wildcard DNS record
- Not a UI — REST API + CLI only
- Not multi-server

## License

MIT
