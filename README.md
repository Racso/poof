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
poof add <name> [flags]              register project + automate GitHub setup
poof apply [-f file] [--dry-run] [--prune]   declarative project sync
poof caddy get|set|delete <name>     manage a project's Caddy snippet
poof caddy list                      list projects with custom Caddy snippets
poof clone <name> <suffix>           clone project as <name>-<suffix> on branch <suffix>
poof config                          show client and server configuration
poof config set <key> [value]        set a client or server configuration value
poof configure <name> [flags]        update project configuration (token is preserved)
poof deploy <name>                   trigger manual redeploy
poof env copy <src> <dst> <mode>     copy env vars (--all, --only, --except, --ask)
poof env get <name>                  list env var keys (comma-separated, values never shown)
poof env set <name> KEY=VALUE        set env vars
poof env unset <name> KEY            remove env var
poof gc [name] [--keep N] [--older-than D] [--all] [--dry-run]   run garbage collection
poof gc set [name] [--keep N] [--older-than D] [--all]   set GC retention policy
poof gc status                       show GC policies
poof gc off [name] | --all           disable GC for a project or globally
poof install                         set up a Poof! server on this machine
poof list                            list all projects and status
poof logs <name> [--lines N]         container log lines
poof migrate workflows [--apply]     one-shot migrations across breaking releases
poof redirect add <from> <to>        add a domain redirect (301)
poof redirect delete <id>            delete a redirect by ID
poof redirect list                   list all redirects
poof refresh <name>                  re-sync GitHub secrets and workflow
poof remove <name>                   remove project, stop container
poof rollback <name>                 redeploy previous image
poof server                          start the daemon
poof server-logs                     show the Poof! server's own logs
poof spell proxy <source> <target>   install a Caddy reverse_proxy on a project (see Spells)
poof status <name>                   project details + last deployment
poof troubleshoot                    diagnose server connectivity issues
poof update both [version]           update server first, then local CLI
poof update local [version]          update the local CLI binary (latest or pinned)
poof update server [version]         update the server (latest or pinned)
poof version                         print client version
poof volume add <name> <mount>       add a volume mount to a project
poof volume list <name>              list volume mounts for a project
poof volume remove <name> <id>       remove a volume mount from a project
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

### Caddy snippets on clones

If the source project has a custom Caddy snippet, clone refuses to guess what to do with it — the snippet usually references the source's own container name (`poof-<source>`), which won't match the clone. You must choose explicitly:

```sh
poof clone myapp test --caddy-yes   # copy the snippet verbatim — you fix references afterwards
poof clone myapp test --caddy-no    # create the clone without a snippet
```

For the common "frontend has an `/api/*` proxy to a backend" case, re-cast the proxy after cloning with `poof spell proxy myapp-test/api myapp-test-engine` (see Spells below).

## Spells

Spells are named recipes built on top of plain Poof commands. The idea: keep the flag surface of `poof add` / `poof configure` small by moving small composite tweaks (snippets, fix-ups, conversions) into a discoverable subcommand instead of inventing a new flag for each.

```sh
poof spell                       # list available spells
poof spell <name> --help         # what a spell does
```

### `poof spell proxy <source> <target>`

Install a Caddy `reverse_proxy` on `<source>` pointing at `<target>`. Useful for fronting a backend through the frontend's own domain to sidestep CORS, or routing a domain to a container that isn't managed by Poof.

**Source** — must reference an existing project:

```
<project>          whole domain proxies
<project>/<path>   path on the project's domain proxies; rest falls through
```

**Target**:

```
<project>            another Poof project — container + port resolved automatically
<container>:<port>   any container on poof-net (Poof-managed or not)
```

```sh
# Frontend at dragonhub.rac.so proxies /api/* to the backend, no CORS
poof spell proxy dragonhub/api dragonhub-engine

# Route a domain to a non-Poof container (e.g. one started by Compose)
poof spell proxy mysite indigo-app-racso:3000
```

By default, the source path is stripped before forwarding so the backend doesn't need to know it's mounted under `/api`. Pass `--keep-prefix` if your backend expects the prefix.

**Spells refuse to overwrite an existing Caddy snippet.** If the source project already has one — from a previous cast or hand-written — the spell errors and tells you how to clear it:

```
$ poof spell proxy dragonhub/api dragonhub-engine
Error: project "dragonhub" already has a Caddy snippet.
  View it:   poof caddy get dragonhub
  Clear it:  poof caddy delete dragonhub
```

This is deliberate: re-casting or accumulating multiple proxies on one project means deleting first. The trade-off is no silent data loss — the spell never edits content you might have authored or previously cast.

## CI modes

The `--ci` flag controls how Poof! sets up GitHub Actions for a project:

- **`--ci yes`** (default) — Poof! commits a standalone push-triggered workflow at `.github/workflows/poof.yml`. Every push to the configured branch builds, pushes to GHCR, and triggers a deploy.
- **`--ci no`** — Poof! does not touch the repo. You're on your own for triggering deploys (call `POST /projects/<name>/deploy` from anywhere).
- **`--ci callable`** — Poof! commits a *reusable* workflow (`on: workflow_call`) instead. You write your own outer workflow that runs tests/lint/matrix builds and then calls this one. Useful when Poof's deploy step is one stage of a larger pipeline.

```sh
poof add myapp --ci callable        # reusable workflow
poof configure myapp --ci no        # stop managing CI for this project
```

`POOF_URL` and `POOF_TOKEN` are set as repo secrets in `yes` and `callable` modes. Re-sync them with `poof refresh <name>` after rotating a token.

## Refreshing GitHub config

Re-sync secrets and workflow files for a project:

```sh
poof refresh myapp
```

Useful after template changes or server upgrades. Skips the workflow commit if the file is already up to date.

## Static sites

For projects that just serve files, skip the container entirely — Caddy serves the files directly from disk. Faster, no RAM, no runtime to crash.

```sh
poof add mysite --static                    # serve repo contents as-is
poof add mysite --static --spa              # SPA: add try_files fallback to /index.html
poof add mysite --static --spa --build      # build first via Dockerfile, then serve
```

**`--static`** turns off the container path. On each deploy, Poof! fetches the repo at the configured branch, extracts the files, and points Caddy at the new directory. Old versions are kept on disk for rollback (subject to the GC policy).

**`--spa`** adds `try_files {path} /index.html` so client-side routes fall back to `index.html`. Required for React/Vue/Svelte SPAs.

**`--build`** runs a Dockerfile in the repo to *produce* the static files (useful when the source needs a build step — Vite, Astro, etc.). The Dockerfile must output to `/poof`; everything in `/poof` is what gets served. Combine with `--static` (and optionally `--spa`).

Convert an existing project to static:

```sh
poof configure mysite --static --spa
poof deploy mysite           # stops the old container, serves files instead
```

Revert back to a container:

```sh
poof configure mysite --no-static
```

## Monorepos

Use `--folder` to point at a subdirectory containing the Dockerfile. The generated GitHub Actions workflow scopes its trigger (and its build context) to that folder — changes to other folders don't redeploy this project.

```sh
poof add myapp-frontend --folder frontend/
poof add myapp-backend  --folder backend/ --port 3000
```

Both projects share the same repo but redeploy independently when their respective folder changes. Combined with `--static` you get a typical fullstack monorepo:

```sh
poof add web --folder web/ --static --spa --build   # SPA frontend
poof add api --folder api/ --port 3000              # backend container
poof spell proxy web/api api                        # frontend /api/* → backend, no CORS
```

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

## Caddy

Caddy fronts every project — it terminates TLS (auto-renewed via ACME), routes hostnames to the right container, and handles `poof redirect` rules. Poof! regenerates the active Caddy config after every project change. You can extend that config without editing it directly, in two ways: per-project snippets (preferred) and manual drop-in files (for services Poof! doesn't manage).

### Per-project snippets (preferred)

Attach a custom Caddy snippet to a project. The snippet is merged into the project's site block, so it runs alongside (or instead of) the route Poof! generates.

```sh
poof caddy get <name>      # download the current snippet to a local file for editing
poof caddy set <name>      # push the edited file back to the server
poof caddy delete <name>   # remove the snippet (Poof's default route returns)
poof caddy list            # list projects that have a custom snippet
```

Typical uses: a `/api/*` reverse_proxy to a sibling backend, a `try_files` directive for clean URLs on a static site, a `header` rule for CORS or caching. For the most common patterns, **prefer a Spell** (next section) — they generate the same snippets but you don't have to write the Caddy yourself.

### Manual Caddyfiles (for non-Poof services)

For services that Poof! doesn't manage at all (e.g. WordPress in a separate Compose stack), drop a `.Caddyfile` into the static config directory (default: `/etc/caddy/conf.d/`). Poof! imports these via Caddy's `import` glob, and they survive every reload.

```caddyfile
# /etc/caddy/conf.d/wordpress.Caddyfile
mysite.com, www.mysite.com {
    reverse_proxy wordpress:80
}
```

The directory must be visible inside the Caddy container (mount it as a volume). An empty directory is fine.

## Garbage collection

Every deploy pulls a fresh Docker image. Without cleanup, disk fills up fast (production saw 342 images / 41 GB before GC existed). Poof! garbage-collects images per a configurable policy.

**Run on demand:**

```sh
poof gc myapp                       # apply myapp's policy now
poof gc --all                       # GC every project + sweep orphans
poof gc myapp --keep 5              # override: keep the 5 most recent
poof gc myapp --older-than 14       # override: delete anything older than 14 days
poof gc --all --dry-run             # show what would be deleted, don't delete
```

When both `--keep` and `--older-than` are set, an image must satisfy **both** conditions to be deleted — it must be outside the keep window AND older than N days. Prevents accidentally nuking recent images.

**Set a policy** (runs automatically after every deploy):

```sh
poof gc set myapp --keep 5          # per-project
poof gc set --all --keep 3          # global default
poof gc set --all --older-than 30   # alternative: age-based default
poof gc status                      # show all policies
poof gc off myapp                   # disable for one project
poof gc off --all                   # disable globally
```

Without an explicit policy, the built-in default is `--keep 3`. The currently running image is never deleted. `--all` also sweeps **orphan images** — images Poof! deployed previously but whose project has since been deleted, renamed, or converted to static.

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
