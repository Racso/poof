> **Superseded on 2026-11-20.** The device-flow login described here was
> removed: the server now holds no GitHub credential at all, and the client
> places the workflow file (see *GitHub access* in CLAUDE.md). Parts B and C
> of this design (OIDC deploys, the job's own token for the pull) remain
> exactly as specified.

# GitHub auth redesign: no server-side GitHub secret

Status: spec approved for implementation (2026-11-18). Experiments in
Racso/poof-test validated every mechanism below (notes #682, #629).

## Goals

1. Self-hostable: anyone runs the `poof` binary on their server and deploys
   from it. No GitHub App to create, no private key to install.
2. Minimal blast radius: nothing long-lived with repo write access rests on
   the server. Whatever does rest there is scoped, expiring, and revocable
   from GitHub in one click.
3. Automation preserved: `poof add` still sets up CI end to end. Acceptable
   manual steps: installing a GitHub App on one's account and ticking repos,
   approving a device code in a browser.

## What the server needs GitHub for, and how each is covered

| Moment | Today | After |
|---|---|---|
| A. Write `.github/workflows/poof*.yml` into the repo (`add`, `refresh`, `clone`, `remove`, settings changes) | PAT or App installation token on the server | Device-flow **user token** of the public Poof App (default), or a fine-grained PAT, or `--no-ci` and commit by hand |
| B. Authenticate the deploy call from CI | Per-repo `POOF_TOKEN` stored as an Actions secret | **OIDC**: the job presents GitHub's JWT; the server verifies it |
| C. Pull the image from ghcr | Server-side registry credential | The job's own **`GITHUB_TOKEN`**, sent in the deploy call, used once |

Result: the only GitHub material at rest on the server is the user's
refresh token (A), encrypted at rest, revocable by uninstalling the App.
B and C store nothing. Actions `Secrets` and `Packages` permissions are no
longer needed by the App.

## B. OIDC deploy authentication

Workflow side (generated template, all three CI modes: push, callable,
static):

```yaml
permissions:
  contents: read
  packages: write
  id-token: write
...
- name: Deploy
  run: |
    POOF_URL="https://poof.example.com"   # baked in at generation time
    JWT=$(curl -sS -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
      "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=$POOF_URL" | jq -r .value)
    curl -fsSL -X POST "$POOF_URL/projects/<name>/deploy" \
      -H "Authorization: Bearer $JWT" \
      -H "X-Poof-Registry-Token: ${{ secrets.GITHUB_TOKEN }}" \
      -d '{"image": "..."}'
```

`POOF_URL` is baked into the generated workflow as plain text (it is not a
secret) so no repo secret or variable is required.

Server side (`authFlex` on `/deploy` and `/deploy/static`):

1. Bearer token parses as a JWT with `iss ==
   https://token.actions.githubusercontent.com`: OIDC path. Otherwise fall
   through to the existing global-token and legacy repo-token checks (kept
   for the migration window; removable later).
2. Verify RS256 signature against the JWKS at
   `https://token.actions.githubusercontent.com/.well-known/jwks`, cached
   in memory with refresh on unknown `kid` (at most once a minute).
3. Check `aud == public_url`, `exp`/`nbf` with 60 s skew.
4. Check `repository` (case-insensitive) equals the project's `repo`, and
   optionally `ref == refs/heads/<branch>` when the project has a branch.
   `repository_id` is recorded on first sight and pinned afterwards, which
   defeats a renamed/recreated repo reusing the name.
5. Log `run_id`, `actor`, `sha` with the deployment for audit.

New dependency: none. Stdlib `crypto/rsa`, `encoding/base64`, `encoding/json`
suffice (the App code already has an RS256 JWT signer; the verifier is the
mirror image).

## C. Pull with the job token

- Deploy request carries the job token in `X-Poof-Registry-Token` (header,
  not body, so it never lands in a stored request log). The server uses it
  for `docker login ghcr.io -u x-access-token` into a per-deploy, throwaway
  `DOCKER_CONFIG` directory, pulls, logs out, deletes the directory. It is
  never persisted.
- The pull happens inside the request. The job waits for the response (it
  already does: `curl -f`). The token is valid for the job's lifetime, so
  the server must not defer the pull.
- `restart`, `pause/resume`, `rollback` use images already on disk. GC must
  keep the current and previous image of every project (verify
  `cmd/gc.go` and the server-side gate already do; add a test).
- No token in the request (manual `poof deploy`, legacy workflow): fall back
  to anonymous pull, then to the legacy server credential if still
  configured. Public images keep working with nothing at all.
- Package name collision: a push to a package that exists but is not linked
  to the repo fails (403 at blob HEAD). `poof add` keeps defaulting the image
  to `ghcr.io/<owner>/<project>`; the generated workflow's first-push failure
  mode is documented in `poof troubleshoot`.

## A. Writing the workflow: device-flow user token

Server settings replace `github-token` / `github-app-*`:

- `github-client-id` (default: the public Poof App's client id, overridable
  for a private App).
- `github-user-auth`: JSON blob `{access_token, expires_at, refresh_token,
  refresh_expires_at, login}` encrypted at rest with a key stored in
  `poof.toml` (`settings_key`, generated by `poof install`; a server
  without it refuses to store the blob). Never returned by `GET /config`;
  masked as `login (expires in N days)`.

CLI:

- `poof login`: `POST /auth/github/device` on the server, which starts the
  device flow (client id only, no secret) and returns `user_code` +
  `verification_uri`. The CLI prints them, the server polls GitHub until
  approval, stores the pair, and the CLI reports `logged in as <login>`.
  `poof logout` deletes the blob.
- Refresh: on every use, if `expires_at` is within 10 min, exchange the
  refresh token. GitHub revokes the old access token on refresh, so the new
  pair is written in one transaction before the old is discarded; a failed
  write keeps the old pair.
- When the refresh token has expired or GitHub answers 401 on a write, the
  server returns 428 with `"run poof login"`; the CLI prints exactly that.
  Deploys are unaffected (B and C do not use this token).

Fallback and alternatives:

- `poof config set github-token <fine-grained PAT>` stays supported
  (Contents RW + Workflows RW, selected repos recommended). The CLI warns
  when the PAT is classic (`ghp_`) and refuses nothing.
- `--no-ci` on `add`, plus `poof ci print <project>` to emit the workflow
  for a manual commit.

Permissions the public App needs after this ships: Contents RW,
Workflows RW, Metadata R. Secrets and Packages are dropped (installations
get a one-click re-approval).

## Migration

1. Server deploys with OIDC and job-token pull enabled. Legacy `POOF_TOKEN`
   auth and server registry credential keep working.
2. `poof refresh <project>` regenerates the workflow in the new shape and
   deletes the repo's `POOF_TOKEN` / `POOF_URL` secrets when no project of
   that repo still uses them.
3. `poof status` lists projects whose last deploy used legacy auth.
4. After every project is refreshed: `poof config unset github-token`,
   delete the PAT on GitHub, remove Secrets + Packages from the App.
5. Later release: drop legacy repo-token auth and `registryCredentials`.

## Code map

- `server/oidc.go` (new): JWKS cache, verifier, claim checks. Tests with a
  generated RSA key and a fake JWKS server.
- `server/server.go`: `authFlex` gains the OIDC branch; deploy handlers read
  `X-Poof-Registry-Token`.
- `server/handlers.go` + `docker/`: `runDeploy` takes an optional registry
  credential; pull with throwaway `DOCKER_CONFIG`.
- `github/github.go`: workflow templates (three modes) switch to OIDC +
  job token; `SetRepoCI` stops writing secrets; `RemoveRepoCI` still deletes
  legacy ones.
- `github/userauth.go` (new): device flow start/poll, refresh, encrypted
  storage. Replaces `github/app.go` (delete; keep `jwt.go` helpers if the
  verifier shares them).
- `server/handlers.go`: `githubAuth()` resolves user token first, PAT
  second; `POST /auth/github/device`, `GET /auth/github`, `DELETE
  /auth/github`.
- `cmd/login.go` (new), `cmd/config.go` (remove app settings, add
  `settings_key` handling in `install.go`), `cmd/add.go` (`--no-ci`),
  `cmd/ci.go` (`print`).
- `README.md`, `CLAUDE.md`, `poof troubleshoot`: the new flow, the App
  install step (slug `poof-app`), package-collision note.

## Plan

1. **OIDC verify + job-token pull** (server + templates). Ship as release
   N; nothing breaks for existing projects. ~1 day.
2. **Device-flow login** (server + CLI), PAT fallback, remove App code.
   Release N+1. ~1 day.
3. **Refresh all rac.so projects**, unset PAT, trim App permissions,
   docs. Half a day.
4. Later: drop legacy paths.

Interim for blocked sessions (before step 1 is live): a 7-day classic PAT
(`repo`, `workflow`, `read:packages`) via `poof config set github-token` on
the running v0.38.0.
