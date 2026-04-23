# Poof!

Lightweight self-hosted deployment daemon. Push to git → deployed.

```
poof add myapp
# next git push → live at myapp.yourdomain.com
```

## How it works

1. `poof add myapp` registers a project. If a GitHub PAT is configured, Poof! also sets `POOF_TOKEN` as a repo secret and commits the deploy workflow into the repo.
2. On every push to `main`, GitHub Actions builds a Docker image, pushes it to GHCR, then calls `POST /projects/myapp/deploy` on your Poof! server.
3. Poof! pulls the image, starts the container on `poof-net`, and pushes the updated routing config to Caddy's admin API. Caddy handles TLS automatically.

No DNS changes needed per project — a single wildcard A record (`*.yourdomain.com → server`) covers everything.

## Requirements

- A Linux server with Docker
- A wildcard DNS A record pointing to the server (for subdomains), or individual DNS records per project
- A `Dockerfile` in each project repo (unless deploying static sites)

## Installation

### Server

One command sets up everything — Caddy, Docker image, config, and starts the server:

```sh
curl -fsSL https://poof.rac.so/install | sh -s server
```

Only requires Docker. The installer will prompt for the API domain (e.g. `poof.yourdomain.com`), generate an auth token, and start both Caddy and Poof!.

### Client

```sh
curl -fsSL https://poof.rac.so/install | sh -s client
```

The installer will prompt for the server URL and token printed by the server install.

Or download a binary directly from [releases](https://github.com/racso/poof/releases).

## Server configuration

The server config lives at `/etc/poof/poof.toml`. The installer creates this automatically, but you can edit it:

```toml
token            = "your-secret-token"             # required; used by CLI to authenticate with the server
public_url       = "https://poof.yourdomain.com"   # Caddy routes this domain to the Poof! API
api_port         = 9000                            # default; omit to keep
data_dir         = "/var/lib/poof"                 # default; omit to keep
caddy_admin_url  = "http://caddy-proxy:2019"       # omit if your Caddy container is named caddy-proxy
caddy_static_dir = "/etc/caddy/conf.d"             # dir for manual Caddyfile snippets (default shown)
```

After the server is running, push the remaining settings from your machine:

```sh
poof config set domain yourdomain.com
poof config set github-user  your-github-username
poof config set github-token ghp_...    # PAT with scopes: repo, workflow, read:packages, delete:packages
```

Run `poof config` at any time to see the current client and server settings.

## Client configuration

The CLI reads from `~/.config/poof/poof.toml` (respects `$XDG_CONFIG_HOME`; Windows: `%AppData%\poof\poof.toml`). Use `poof config set` to write settings, or edit the file directly:

```toml
server = "https://poof.yourdomain.com"
token  = "your-secret-token"
```

### Profiles

Named profiles let you switch between multiple Poof! servers:

```toml
# default
server = "https://poof.personal.com"
token  = "personal-token"

[work]
server = "https://poof.work.com"
token  = "work-token"
```

```sh
poof --profile work list
```

Or via environment:

```sh
export POOF_PROFILE=work
poof --profile-env list   # errors immediately if $POOF_PROFILE is unset
```

A profile can also import from a separate file:

```toml
[work]
import = "~/.config/poof/work.toml"
```

## CLI

```
poof add <name> [flags]            register project + automate GitHub setup
poof apply [-f file] [--dry-run] [--prune]   declarative project sync
poof clone <name> <suffix>         clone project as <name>-<suffix> on branch <suffix>
poof config                        show client and server configuration
poof config set <key> [value]      set a client or server configuration value
poof configure <name> [flags]      update project configuration (token is preserved)
poof deploy <name>                 trigger manual redeploy
poof env copy <src> <dst> <mode>   copy env vars (--all, --only, --except, --ask)
poof env get <name>                list env var keys (comma-separated, values never shown)
poof env set <name> KEY=VALUE      set env vars
poof env unset <name> KEY          remove env var
poof install                       set up a Poof! server on this machine
poof list                          list all projects and status
poof logs <name> [--lines N]       container log lines
poof redirect add <from> <to>      add a domain redirect (301)
poof redirect delete <id>          delete a redirect by ID
poof redirect list                 list all redirects
poof refresh <name>                re-sync GitHub secrets and workflow
poof remove <name>                 remove project, stop container
poof rollback <name>               redeploy previous image
poof server                        start the daemon
poof status <name>                 project details + last deployment
poof update both [version]         update server first, then local CLI
poof update local [version]        update the local CLI binary (latest or pinned)
poof update server [version]       update the server (latest or pinned)
poof version                       print client version
poof volume add <name> <mount>     add a volume mount to a project
poof volume list <name>            list volume mounts for a project
poof volume remove <name> <id>     remove a volume mount from a project
```

Global flags (all client commands):

```
--profile <name>   use a named profile from the client config
--profile-env      read the profile name from $POOF_PROFILE (errors if unset)
```

All flags have smart defaults — `poof add myapp` is usually enough.

## Cloning (environments)

Clone a project to create a test, staging, or other parallel environment:

```sh
poof clone myapp test              # creates myapp-test, deploys from "test" branch
poof clone myapp staging --env --all  # same, plus copies all env vars
```

The clone inherits the source project's repo, image, port, subpath, and folder. The domain is automatically set to `<name>-<suffix>.<root-domain>`, and the branch is set to the suffix. GitHub Actions workflow is set up automatically.

Copy env vars selectively:

```sh
poof clone myapp test --env --only API_KEY,FEATURE_FLAGS
poof clone myapp test --env --except DATABASE_URL
poof clone myapp test --env --ask     # interactive per-key confirmation
```

You can also copy env vars between any two projects independently:

```sh
poof env copy myapp myapp-test --all
poof env copy myapp myapp-test --except DATABASE_URL,REDIS_HOST
```

Use `poof env get <name>` to see available keys (comma-separated, ready for `--only`/`--except`).

## Refreshing GitHub config

Re-sync secrets and workflow files for a project:

```sh
poof refresh myapp
```

Useful after template changes or server upgrades. Skips the workflow commit if the file is already up to date.

## Subpath routing

By default, projects are only reachable at their subdomain (`myapp.yourdomain.com`). Subpath routing additionally makes a project reachable at `yourdomain.com/myapp/*`, in one of two modes:

- **`redirect`** — `yourdomain.com/myapp/*` issues a 301 redirect to `myapp.yourdomain.com/*`.
- **`proxy`** — requests are transparently proxied to the container. The app must handle being served from a subpath.

```sh
poof add myapp --subpath=redirect
poof configure myapp --subpath=proxy
poof deploy myapp   # redeploy required for routing changes to take effect
```

Set the server-wide default in `poof.toml`:

```toml
subpath_default = "redirect"   # disabled | redirect | proxy (default: disabled)
```

## Volumes

Persistent volume mounts survive container recreations and redeployments.

```sh
poof volume add myapp /app/data                    # managed mount
poof volume add myapp /mnt/uploads:/app/uploads    # explicit mount
poof volume list myapp
poof volume remove myapp <id>
poof deploy myapp   # redeploy to apply changes
```

**Managed mounts** — only a container path is given. Poof! creates and owns the host directory at `/var/lib/poof/<project>/<container-path>`. When removing a managed volume, you will be asked whether to delete the host data (`--data-delete` / `--data-keep` to skip the prompt).

**Explicit mounts** — `host/path:container/path` format. You control the host directory; Poof! never touches it.

## Domain redirects

Redirects send one domain to another with a 301, independent of any project:

```sh
poof redirect add www.mysite.com mysite.com
poof redirect list
poof redirect delete 1
```

## Manual Caddy routes

Poof! regenerates its Caddyfile on every sync, but you can add routes for services not managed by Poof! (e.g. WordPress running via Compose) by dropping `.Caddyfile` files into the static config directory (default: `/etc/caddy/conf.d/`).

```sh
# On the host, create the directory and mount it into Caddy:
mkdir -p /etc/caddy/conf.d
```

Then add a file per service:

```caddyfile
# /etc/caddy/conf.d/wordpress.Caddyfile
oscarhumbertogomez.com, www.oscarhumbertogomez.com {
    reverse_proxy wordpress:80
}
```

These files are imported via Caddy's `import` glob directive and survive Poof! reloads. The directory must be accessible inside the Caddy container (mount it as a volume). If the directory is empty, Caddy handles it gracefully.

## Declarative projects file

Declare all projects in an INI file and apply it idempotently:

```ini
[myapp]

[api]
domain = api.yourdomain.com
port   = 3000

[worker]
image  = ghcr.io/myorg/worker
branch = stable
```

```sh
poof apply                     # apply poof.ini in current directory
poof apply -f /path/to/file
poof apply --dry-run           # preview changes without applying
poof apply --prune             # also remove projects absent from the file
```

## License

MIT
