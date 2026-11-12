#!/bin/sh
# Deploy the checked-out commit to Fly.io and confirm the app reports it.
#
#   packaging/fly/deploy.sh staging      any pushed commit; test it at https://staging.bookmeet.date
#   packaging/fly/deploy.sh production   only a commit on origin/bookmeet that staging is running
#
# Needs: flyctl signed in (fly auth login) and a clean checkout. Bump VERSION (1.N) in the push.
# Ask the owner before a production deploy: it replaces the live app.
# After a production deploy it adds a release line to the venture's LOG.md in the owner's
# Obsidian vault (BOOKMEET_VAULT, else the iMac or laptop location below). No vault on this
# machine: it says so, and the
# line must be added by hand.
set -eu
cd "$(dirname "$0")/../.."
target=${1:-}
case "$target" in
  staging)    app=bookmeet-staging; config=packaging/fly/fly.staging.toml ;;
  production) app=bookmeet;         config=packaging/fly/fly.toml ;;
  *) echo "Usage: packaging/fly/deploy.sh staging|production" >&2; exit 2 ;;
esac
# The vault syncs between machines but sits in a different place on each: shared on the iMac,
# in Documents on a laptop. BOOKMEET_VAULT overrides both.
vault=${BOOKMEET_VAULT:-}
if [ -z "$vault" ]; then
  vault="/Users/Shared/ObsidianVault/10 Ventures/bookmeet"
  [ -d "$vault" ] || vault="$HOME/Documents/Obsidian Vault/10 Ventures/bookmeet"
fi
version=$(tr -d '[:space:]' < VERSION)
commit=$(git rev-parse HEAD)

# running <app>: the /version answer of that app, asked at its fly.dev address (staging's own
# address sits behind a login). A sleeping machine starts on the request, hence the retries.
running() {
  n=0
  while [ "$n" -lt 30 ]; do
    if out=$(curl -fsS -m 8 "https://$1.fly.dev/version" 2>/dev/null); then echo "$out"; return 0; fi
    n=$((n + 1)); sleep 4
  done
  return 1
}

[ -z "$(git status --porcelain)" ] || { echo "Uncommitted changes: commit them first." >&2; exit 1; }
git fetch --quiet origin
[ -n "$(git branch -r --contains "$commit")" ] || { echo "This commit is not on GitHub: push first." >&2; exit 1; }
if [ "$target" = production ]; then
  git merge-base --is-ancestor "$commit" origin/bookmeet || { echo "Production takes commits on origin/bookmeet only." >&2; exit 1; }
  case "$(running bookmeet-staging || true)" in
    *"$commit"*) ;;
    *) echo "Staging is not running $commit. Run 'packaging/fly/deploy.sh staging' and check it first." >&2; exit 1 ;;
  esac
fi

echo "Deploying $version ($commit) to $target ($app)"
fly deploy . --config "$config" --app "$app" --ha=false \
  --build-arg "VERSION=$version" --build-arg "COMMIT=$commit"

case "$(running "$app" || true)" in
  *"$commit"*) echo "$target now runs $version ($commit)" ;;
  *)
    echo "$target does not report $commit. Check: fly logs --app $app" >&2
    echo "To go back: fly releases --app $app --image, then fly deploy --app $app --config $config --image <previous image>." >&2
    exit 1 ;;
esac

if [ "$target" = staging ]; then
  echo "Check it at https://staging.bookmeet.date, then: packaging/fly/deploy.sh production"
  exit 0
fi
line="- $(date +%F) release $version ($(git rev-parse --short HEAD)) deployed to Fly.io: $(git log -1 --format=%s)"
if [ -w "$vault/LOG.md" ]; then
  printf '%s\n' "$line" >> "$vault/LOG.md"
  echo "Logged in $vault/LOG.md. If this release changed hosting, settings or how to release, correct CONTEXT.md there too."
else
  echo "No vault at $vault. Add this line to the venture's LOG.md by hand:"
  echo "$line"
fi
