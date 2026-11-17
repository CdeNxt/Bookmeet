# Distribution packaging — agent notes

Moved here unchanged from the root `CLAUDE.md`. The root file lists when to read this.

## Distribution packaging (packaging/)

- One channel per directory: `packaging/ns8/` is the NethServer 8 module
  (imageroot actions, systemd unit, Vue settings UI, robot tests). Future
  channels (Umbrel, Coolify, TrueNAS, etc.) get siblings, never a mega-PR.
- The NS8 module is a thin wrapper around the published app image, built by
  `.github/workflows/ns8-module.yml` with the same tag scheme as
  `docker-publish.yml`. Release tags pin module and app to the same version.
- The app image ref lives in the `org.nethserver.images` label; at install
  the cluster exposes it as `${CALNODE_IMAGE}` and the unit + configure read
  it from there. Never hardcode an app tag in `packaging/ns8/`.
