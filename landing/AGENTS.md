# Poof! — Agent Reference

Lightweight self-hosted deployment daemon: one Linux server with Docker,
fronted by Caddy for automatic TLS. Register a GitHub repo once; every
push builds an image in GitHub Actions, pushes it to GHCR, and deploys it
at `<name>.<yourdomain>`. Humans and agents drive it through the same CLI;
this document is the agent-facing summary and is reachable at:

- `https://poof.rac.so/AGENTS.md`
- `https://poof.rac.so/llms.txt` (index — points back here)

Full documentation: <https://github.com/racso/poof#readme>

## What Poof! is (and is not)

- One operator, one server, many projects. Not Kubernetes, not a build
  system (CI builds; Poof! deploys), not a multi-tenant PaaS.
- The server owns a SQLite store and pushes routing config to Caddy's
  admin API. A single wildcard DNS record covers all subdomains.

## Install

```sh
# Server (on the Linux host; requires Docker):
curl -fsSL https://poof.rac.so/install | sh -s server

# Client (on your machine):
curl -fsSL https://poof.rac.so/install | sh -s client
```

The client reads `~/.config/poof/poof.toml` (`server`, `token`; optional
named profiles selected with `--profile <name>` or `POOF_PROFILE` +
`--profile-env`). If commands fail with auth errors, check that file.

## Core workflow

```sh
poof add myapp          # register; sets repo secrets + commits CI workflow
git push                # → builds, pushes to GHCR, deploys automatically
# live at https://myapp.yourdomain.com
```

Requirements on the repo: a `Dockerfile` (unless `--static`), and a
GitHub PAT configured on the server for the CI automation.

## CLI quick reference

Project lifecycle:

```
poof add <name> [--domain --repo --branch --port --folder --static --spa --build --subpath --ci]
poof configure <name> [flags]        change any field; only passed flags mutate
poof clone <src> <suffix>            parallel env from branch <suffix> (staging, test)
poof remove <name>                   delete registration + stop container
poof apply [-f poof.ini] [--dry-run] [--prune]   declarative sync
```

Deploy & observe:

```
poof deploy <name> [--image <tag>]   manual redeploy
poof rollback <name>                 redeploy previous successful image
poof status <name> | poof list       status: running | stopped | paused
poof logs <name> [-n N]              container logs
```

Incident response (pause / snapshot / resume):

```
poof pause <name>      all routes 503, container stopped (not removed),
                       registration and config untouched
poof snapshot <name>   forensic capture: writable layer → local image
                       (poof-snapshot/<name>:<ts>) + logs + inspect dump;
                       GC never touches snapshots
poof resume <name>     exact previous state restored, single command
```

Deploys made while paused are **staged**: the new container is created but
not started, so a fix can land before going back online — `poof resume`
starts it. Recommended flow: pause → snapshot → deploy fix → resume.

Config, env, routing:

```
poof config set <key> [value]        server|token (local); domain|github-user|github-token (server)
poof env set|get|unset|copy          env vars (values never echoed back)
poof volume add|list|remove          persistent mounts
poof net create|add|ls|...           private inter-project Docker networks
poof redirect add|list|delete        domain-level 301s
poof caddy get|set|delete|list       per-project custom Caddy snippet
poof spell proxy|clean-urls|to-static   common recipes (see `poof spell --help`)
poof gc [--keep N] [--older-than D]  image garbage collection
poof update local|server|both        self-update
```

## Notes for agents

- Static sites: `--static` serves files via Caddy (no container); `--spa`
  adds an `index.html` fallback that composes with custom `handle` routes
  in a Caddy snippet (e.g. an `/api/*` reverse_proxy on the same domain).
- Monorepos: `--folder <dir>` scopes CI triggers and builds per project.
- Most mutations need a redeploy to take effect on the container
  (env vars, volumes, networks, subpath).
- The HTTP API behind the CLI is bearer-token authed and not a public
  surface; drive Poof! through the CLI.
