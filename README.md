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

## Server configuration

Create `/etc/poof/poof.toml` on the server:

```toml
domain          = "yourdomain.com"
api_port        = 9000
data_dir        = "/var/lib/poof"
public_url      = "https://poof.yourdomain.com"  # set as POOF_URL repo secret
subpath_default = "redirect"                      # default subpath mode for new projects (see Subpath routing)

[github]
user  = "your-github-username"
token = "ghp_..."               # PAT with scopes: repo, workflow, read:packages, delete:packages

[auth]
token = "your-secret-token"     # used by CLI to authenticate with the server
```

## Client configuration

The CLI reads from `~/.config/poof/poof.toml` (Linux/macOS: respects `$XDG_CONFIG_HOME`; Windows: `%AppData%\poof\poof.toml`). Run `poof config` to print the exact path on your machine.

```toml
server = "https://poof.yourdomain.com"
token  = "your-secret-token"
```

### Profiles

Named profiles let you switch between multiple Poof! servers. Each profile is a top-level TOML table:

```toml
# default (used when no --profile flag is given)
server = "https://poof.personal.com"
token  = "personal-token"

[work]
server = "https://poof.work.com"
token  = "work-token"

[staging]
server = "https://poof-staging.work.com"
token  = "staging-token"
```

Use a profile:

```sh
poof --profile work list
poof --profile staging deploy myapp
```

Or set `POOF_PROFILE` in the environment and use `--profile-env` to read it:

```sh
export POOF_PROFILE=work
poof --profile-env list
```

`--profile-env` is designed for scripts and CI: it errors immediately if `$POOF_PROFILE` is not set, so there is no silent fallback to the default profile. `--profile` and `--profile-env` are mutually exclusive.

#### Importing a profile from a separate file

A profile can import its settings from a separate TOML file instead of inlining them. This is useful when you share credentials across machines via a secrets manager or a mounted file:

```toml
# ~/.config/poof/poof.toml
server = "https://poof.personal.com"
token  = "personal-token"

[work]
import = "~/.config/poof/work.toml"
```

```toml
# ~/.config/poof/work.toml
server = "https://poof.work.com"
token  = "work-token"
```

`poof --profile work list` loads `work.toml` and uses its `server` and `token`. The imported file is a plain client config file — the same format as the main file, just without profiles.

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
poof update <name> [flags]     update project configuration (token is preserved)
poof remove <name>             remove project, stop container
poof list                      list all projects and status
poof status <name>             project details + last deployment
poof deploy <name>             trigger manual redeploy
poof rollback <name>           redeploy previous image
poof logs <name> [--lines N]   container log lines
poof env get <name>            list env var keys (values never shown)
poof env set <name> KEY=VALUE  set env vars
poof env unset <name> KEY      remove env var
poof config                    print the client config file path
poof server                    start the daemon
```

Global flags (all client commands):

```
--profile <name>   use a named profile from the client config
--profile-env      read the profile name from $POOF_PROFILE (errors if unset)
```

All flags have smart defaults — `poof add myapp` is usually enough.

## Subpath routing

By default, projects are only reachable at their subdomain (`myapp.yourdomain.com`). Subpath routing additionally makes a project reachable at `yourdomain.com/myapp/*`, in one of two modes:

- **`redirect`** — `yourdomain.com/myapp/*` issues a 301 redirect to `myapp.yourdomain.com/*`. App compatibility is perfect.
- **`proxy`** — requests to `yourdomain.com/myapp/*` are transparently proxied to the container. The app must be able to handle being served from a subpath (no path-prefix-unaware asset links or redirects).

Set the mode per project at creation time or later:

```sh
poof add myapp --subpath=redirect
poof update myapp --subpath=proxy   # change mode; token is preserved
poof deploy myapp                   # redeploy required — labels are applied at container start
```

Set the server-wide default for new projects in `poof.toml`:

```toml
subpath_default = "redirect"   # disabled | redirect | proxy (default: disabled)
```

## Declarative projects file

Instead of managing projects imperatively, you can declare all projects in an INI file and apply it idempotently:

```ini
[myapp]

[api]
domain = api.yourdomain.com
port   = 3000

[worker]
image  = ghcr.io/myorg/worker
branch = stable
```

Each section is a project name. All fields are optional — omitted fields use the same smart defaults as `poof add`. Secrets (env vars, per-project tokens) are never stored in this file.

```sh
poof apply                     # apply poof.ini in current directory
poof apply -f /path/to/file    # explicit path
poof apply --dry-run           # preview changes without applying
poof apply --prune             # also remove projects absent from the file
```

`poof apply` adds new projects, updates changed ones (redeploying running containers), and is a no-op for anything already matching. Without `--prune`, projects on the server but not in the file are left untouched.

## What Poof! is not

- Not a build system — Dockerfiles are required (by design)
- Not a DNS manager — use a wildcard DNS record
- Not a UI — REST API + CLI only
- Not multi-server

## License

MIT
