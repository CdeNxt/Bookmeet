# Home Assistant deployment (Bookmeet)

Bookmeet runs on a Raspberry Pi under Home Assistant as the local app `local_calnode`
(`/local_apps/calnode`: `Dockerfile`, `invite-links.patch`, prebuilt `calnode-bookmeet-arm64`,
`run.sh`). Live version check: `https://bookmeet.date/version` returns `version` and `commit`.

## Release and deploy (the normal path)

1. Land and push the change on `bookmeet`. Run the checks in `AGENTS.md`.
2. Bump `packaging/home-assistant/version.txt` (`0.13.0+bookmeet.N`, N+1) and commit + push it.
   `upstream-base.txt` is the pinned upstream tag; do not change it outside an upstream upgrade.
3. Wait for CI (`ci.yml`) to go green. It builds and checksums the ARM64 binary for that commit.
4. Run `packaging/home-assistant/deploy.sh` from a clean checkout of the pushed commit.
   **Ask the owner before running it: it replaces the live app.**

`deploy.sh` deploys exactly the CI-built binary for `HEAD` (never a local build), regenerates the
Dockerfile and `invite-links.patch` with `package.py`, copies them over SSH, verifies SHA-256 on
both ends, copies the previous files to `bookmeet-<prev>-recovery/`, runs
`ha apps rebuild local_calnode` + `ha apps start local_calnode`, and polls `/version` for the new
commit. If the new build does not report it, the previous files are restored and rebuilt, and the
script exits non-zero.

Environment overrides: `HA_HOST` (default `homeassistant`), `HA_PORT` (22), `HA_USER` (`root`),
`HA_KEY` (`~/.ssh/bookmeet_ha_ed25519`), `PUBLIC_URL`, `REPO`.

Requirements: `gh` logged in, `python3`, `ssh`/`scp`, and the private deploy key on the machine
running the script. The key is not in the repo. If it is missing, see "Access" below.

## Access

- The Terminal & SSH app in Home Assistant must be running with the deploy public key in its
  `authorized_keys` option and port 22 mapped. Home Assistant renamed "Add-ons" to **Apps**
  (`ha apps ...`, `/config/app/core_ssh/config`).
- Only the owner changes app settings and security settings in Home Assistant. Do not edit them
  yourself; tell the owner what to change.
- To create a new key pair: `ssh-keygen -t ed25519 -N "" -f ~/.ssh/bookmeet_ha_ed25519`, then the
  owner adds the `.pub` line to the app's `authorized_keys` (use the app's "Edit in YAML"; the list
  field has failed to save).
- Home Assistant cannot reach the developer's Mac over the network, so always push from the
  workstation (`scp`/`ssh`). Do not plan on Home Assistant pulling from it.

## Manual fallback (no SSH)

Download the CI artifact `bookmeet-pi-<sha>` for the commit, then on Home Assistant (Terminal &
SSH app) in `/local_apps/calnode`: verify `calnode-bookmeet-arm64` against `SHA256SUMS`, copy the
current `Dockerfile`, `invite-links.patch` and binary into `bookmeet-<prev>-recovery/`, replace
them with the output of `package.py`, then `ha apps rebuild local_calnode && ha apps start
local_calnode` and confirm `/version`. Restore from the recovery folder if it does not come up.

## Gotchas

- `package.py` refuses to run on a dirty tree and excludes `packaging/home-assistant`, `.github`,
  `AGENTS.md`, `BOOKMEET.md`, `branding` and `.gitignore` from the exported patch.
- The scheduled updater app (`local_calnode_updater`) rebuilds from source with
  `invite-links.patch` when a new upstream release appears. A stale patch there would silently drop
  newer Bookmeet features and migrations, so `deploy.sh` always refreshes it. Keep it current.
- Database migrations only ever add columns; rolling the app back leaves data intact. Back up
  before any migration that is not additive.
- Built-in/CI builds need `CGO_ENABLED=0 GOOS=linux GOARCH=arm64`; the host is arm64.
