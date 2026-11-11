#!/bin/sh
# Deploy the checked-out commit to Fly.io and confirm the live site reports it.
# Needs: flyctl signed in (fly auth login), a clean checkout, and the commit pushed to origin/bookmeet.
# Bump VERSION (1.N) in the same push. Ask the owner first: this replaces the live app.
set -eu
cd "$(dirname "$0")/../.."
app=bookmeet
url=${PUBLIC_URL:-https://bookmeet.date}
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
  case "$live" in *"$commit"*) echo "Live: $live"; exit 0 ;; esac
  i=$((i + 1)); sleep 4
done
echo "The site does not report $commit yet. Check: fly logs --app $app" >&2
echo "To go back: fly releases --app $app, then fly deploy --image <previous image>." >&2
exit 1
