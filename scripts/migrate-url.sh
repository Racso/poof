#!/bin/sh
# migrate-url.sh — Update Poof!'s public URL across all deployed GitHub repos
# and the local client config.
#
# Usage:
#   scripts/migrate-url.sh <new-url> [--dry-run]
#
# Requirements: poof CLI, gh CLI (authenticated with repo scope)
#
# What this script does NOT handle (manual steps — see output at the end):
#   - Updating public_url in /etc/poof/poof.toml on the server
#   - Updating your Caddy / infrastructure config for the new hostname

set -e

# --- Args ---

NEW_URL=""
DRY_RUN=0

for arg in "$@"; do
    case "$arg" in
        --dry-run) DRY_RUN=1 ;;
        http*) NEW_URL="$arg" ;;
        *) echo "Unknown argument: $arg" >&2; exit 1 ;;
    esac
done

if [ -z "$NEW_URL" ]; then
    echo "Usage: $0 <new-url> [--dry-run]" >&2
    exit 1
fi

NEW_URL="${NEW_URL%/}"  # strip trailing slash

# --- Helpers ---

run() {
    if [ "$DRY_RUN" -eq 1 ]; then
        echo "  [dry-run] $*"
    else
        "$@"
    fi
}

# --- Detect old URL from local config ---

CONFIG_PATH=$(poof config)
if [ ! -f "$CONFIG_PATH" ]; then
    echo "error: poof config not found at $CONFIG_PATH" >&2
    exit 1
fi

OLD_URL=$(awk -F'"' '/^server[[:space:]]*=/ { print $2; exit }' "$CONFIG_PATH")
if [ -z "$OLD_URL" ]; then
    echo "error: could not read 'server' from $CONFIG_PATH" >&2
    exit 1
fi

OLD_URL="${OLD_URL%/}"

echo "Migrating Poof! public URL"
echo "  from: $OLD_URL"
echo "    to: $NEW_URL"
echo ""

if [ "$OLD_URL" = "$NEW_URL" ]; then
    echo "URLs are identical. Nothing to do."
    exit 0
fi

# --- Update POOF_URL secret in all GitHub repos ---

PROJECTS=$(poof list 2>/dev/null | awk 'NR > 2 && NF > 0 { print $1 }')

if [ -z "$PROJECTS" ]; then
    echo "No projects found — skipping GitHub secret update."
else
    echo "Updating POOF_URL secret in GitHub repos..."
    for name in $PROJECTS; do
        repo=$(poof status "$name" 2>/dev/null | awk '/^repo:[[:space:]]/ { print $2; exit }')
        if [ -z "$repo" ]; then
            echo "  $name: could not determine repo, skipping"
            continue
        fi
        printf "  %-20s %s\n" "$name" "$repo"
        run gh secret set POOF_URL --body "$NEW_URL" --repo "$repo"
    done
fi

echo ""

# --- Update local client config ---

echo "Updating local client config ($CONFIG_PATH)..."
if [ "$DRY_RUN" -eq 1 ]; then
    echo "  [dry-run] sed s|$OLD_URL|$NEW_URL| $CONFIG_PATH"
else
    tmp=$(mktemp)
    sed "s|server = \"$OLD_URL\"|server = \"$NEW_URL\"|g" "$CONFIG_PATH" > "$tmp"
    mv "$tmp" "$CONFIG_PATH"
fi

echo ""

# --- Manual steps ---

cat <<EOF
Done. Two manual steps still required:

  1. On the server, update public_url in /etc/poof/poof.toml:

         public_url = "$NEW_URL"

     Then restart the Poof container:

         docker restart poof

  2. Update your Caddy / infrastructure config to route the new hostname
     to the Poof container and redeploy.

EOF

if [ "$DRY_RUN" -eq 1 ]; then
    echo "(dry run — no changes were made)"
fi
