#!/bin/sh
# Deploy the checked-out commit to Fly.io and confirm the live site reports it.
# Needs: flyctl signed in (fly auth login), a clean checkout, and the commit pushed to origin/bookmeet.
# Bump VERSION (1.N) in the same push. Ask the owner first: this replaces the live app.
# On success it adds a release line to the venture's LOG.md in the owner's Obsidian vault
# (BOOKMEET_VAULT, default below). No vault on this machine: it says so, and the line must be
# added by hand.
set -eu
cd "$(dirname "$0")/../.."
app=bookmeet
url=${PUBLIC_URL:-https://bookmeet.date}
vault=${BOOKMEET_VAULT:-/Users/Shared/ObsidianVault/10 Ventures/bookmeet}
[ -z "$(git status --porcelain)" ] || { echo "Uncommitted changes: commit them first." >&2; exit 1; }
git fetch --quiet origin bookmeet
git merge-base --is-ancestor HEAD origin/bookmeet || { echo "HEAD is not on origin/bookmeet: push first." >&2; exit 1; }
version=$(tr -d '[:space:]' < VERSION)
commit=$(git rev-parse HEAD)
echo "Deploying $version ($commit) to $app"
fly deploy . --config packaging/fly/fly.toml --app "$app" --ha=false \
  --build-arg "VERSION=$version" --build-arg "COMMIT=$commit"
i=0
while [ "$i" -lt 30 ]; do
  live=$(curl -fsS -m 5 "$url/version" 2>/dev/null || true)
  case "$live" in *"$commit"*)
    echo "Live: $live"
    line="- $(date +%F) release $version ($(git rev-parse --short HEAD)) deployed to Fly.io: $(git log -1 --format=%s)"
    if [ -w "$vault/LOG.md" ]; then
      printf '%s\n' "$line" >> "$vault/LOG.md"
      echo "Logged in $vault/LOG.md. If this release changed hosting, settings or how to release, correct CONTEXT.md there too."
    else
      echo "No vault at $vault. Add this line to the venture's LOG.md by hand:"
      echo "$line"
    fi
    exit 0 ;;
  esac
  i=$((i + 1)); sleep 4
done
echo "The site does not report $commit yet. Check: fly logs --app $app" >&2
echo "To go back: fly releases --app $app, then fly deploy --image <previous image>." >&2
exit 1
