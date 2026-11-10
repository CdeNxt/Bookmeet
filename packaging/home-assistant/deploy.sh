#!/usr/bin/env bash
# Deploy the tested CI build of HEAD to the Home Assistant Bookmeet add-on over SSH.
#
#   packaging/home-assistant/deploy.sh
#
# Needs: gh (logged in), python3, ssh/scp, and the Terminal & SSH add-on reachable with the
# deploy key. Override with HA_HOST, HA_PORT, HA_USER, HA_KEY, PUBLIC_URL.
#
# It deploys exactly what CI built and tested for HEAD (never a local build), verifies
# checksums on both ends, keeps the previous files in a recovery folder, and restores them
# automatically if the rebuilt add-on does not report the new commit.
set -euo pipefail

HA_HOST=${HA_HOST:-homeassistant}
HA_PORT=${HA_PORT:-22}
HA_USER=${HA_USER:-root}
HA_KEY=${HA_KEY:-$HOME/.ssh/bookmeet_ha_ed25519}
PUBLIC_URL=${PUBLIC_URL:-https://bookmeet.date}
REPO=${REPO:-CdeNxt/Bookmeet}
ADDON_DIR=/local_apps/calnode

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
cd "$root"

[ -z "$(git status --porcelain)" ] || { echo "Commit or stash changes first." >&2; exit 1; }
sha=$(git rev-parse HEAD)
git fetch -q origin
git merge-base --is-ancestor "$sha" "origin/$(git rev-parse --abbrev-ref HEAD)" \
  || { echo "HEAD is not pushed; CI has not built it." >&2; exit 1; }
version=$(tr -d '\n' < "$here/version.txt")
patchno=${version##*bookmeet.}

echo "Waiting for CI on ${sha:0:7} ($version)..."
run=$(gh run list --repo "$REPO" --commit "$sha" --workflow CI --json databaseId,status,conclusion --jq '.[0]')
[ -n "$run" ] || { echo "No CI run found for $sha." >&2; exit 1; }
run_id=$(printf '%s' "$run" | python3 -c 'import json,sys;print(json.load(sys.stdin)["databaseId"])')
gh run watch "$run_id" --repo "$REPO" --exit-status >/dev/null

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
gh run download "$run_id" --repo "$REPO" -n "bookmeet-pi-$sha" -D "$work/ci"
(cd "$work/ci" && shasum -a 256 -c SHA256SUMS)

python3 "$here/package.py" --binary "$work/ci/calnode-bookmeet-arm64" --output "$work/pkg.tar.gz" >/dev/null
mkdir "$work/pkg" && tar -xzf "$work/pkg.tar.gz" -C "$work/pkg"
sums=$(cd "$work/pkg" && shasum -a 256 Dockerfile invite-links.patch calnode-bookmeet-arm64)

ssh_opts=(-i "$HA_KEY" -p "$HA_PORT" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new)
target="$HA_USER@$HA_HOST"
stage=/tmp/bookmeet-deploy
ssh "${ssh_opts[@]}" "$target" "rm -rf $stage && mkdir -p $stage"
scp -q -P "$HA_PORT" -i "$HA_KEY" -o IdentitiesOnly=yes -o BatchMode=yes \
  "$work/pkg/Dockerfile" "$work/pkg/invite-links.patch" "$work/pkg/calnode-bookmeet-arm64" "$target:$stage/"

echo "Applying on Home Assistant..."
ssh "${ssh_opts[@]}" "$target" "SUMS='$sums' SHA='$sha' PATCHNO='$patchno' PUBLIC_URL='$PUBLIC_URL' ADDON_DIR='$ADDON_DIR' STAGE='$stage' sh -s" <<'REMOTE'
set -eu
cd "$STAGE"
printf '%s\n' "$SUMS" | sha256sum -c -
cd "$ADDON_DIR"
grep -qx 'ARG BUILD_MODE=prebuilt' "$STAGE/Dockerfile"
prev=$(sed -n 's/.*run-ha-bookmeet-\([0-9]*\)\.sh.*/\1/p' Dockerfile | head -1)
[ -n "$prev" ] || { echo "cannot read the current build number" >&2; exit 1; }
rec="bookmeet-$prev-recovery"
[ -e "$rec" ] && rec="$rec-$(date +%s)"
mkdir "$rec"
cp Dockerfile invite-links.patch calnode-bookmeet-arm64 "$rec/"
cp "$STAGE/Dockerfile" "$STAGE/invite-links.patch" "$STAGE/calnode-bookmeet-arm64" .
if ha apps rebuild local_calnode && ha apps start local_calnode; then
  for i in 1 2 3 4 5 6 7 8; do
    result=$(curl --max-time 6 -fsS "$PUBLIC_URL/version") || result=
    if printf '%s' "$result" | grep -q "$SHA"; then
      printf '%s\n' "$result"; echo "DEPLOY-OK (previous files in $ADDON_DIR/$rec)"; rm -rf "$STAGE"; exit 0
    fi
    sleep 5
  done
fi
echo "New build did not come up; restoring $rec" >&2
cp "$rec/Dockerfile" "$rec/invite-links.patch" "$rec/calnode-bookmeet-arm64" .
ha apps rebuild local_calnode && ha apps start local_calnode
exit 1
REMOTE
