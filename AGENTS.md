# Bookmeet maintenance

This is the maintained Bookmeet fork of Calnode. The production customization branch is `bookmeet`; `origin` is the Bookmeet fork and `upstream` is Calnode/calnode. Preserve upstream history.

- Make Bookmeet changes in this checkout. Keep the public skin, copy settings, appearance, effects, and Google manage-link behavior together with their tests.
- Settings and credentials belong in app data, not source code. Never commit `.env`, databases, keys, local deployment notes, or live booking management tokens. `local/` is deliberately ignored.
- For upstream upgrades, merge a tagged upstream release on a candidate branch, resolve schema/numbered migration conflicts deliberately, and open a PR to `bookmeet`. Do not force-sync over Bookmeet commits.
- Run appearance tests, frontend type-check/build, and the relevant Go suites. CI includes visual tests, full Go checks, and an ARM64 artifact. Inspect live UI changes at desktop and phone sizes where relevant.
- The user wants completed updates pushed to the fork. After validation, commit the authorized changes and push the working branch; do not leave completed production changes only in a temporary chat folder.
- To update the app on the owner's Home Assistant, follow [`packaging/home-assistant/README.md`](packaging/home-assistant/README.md) and use `packaging/home-assistant/deploy.sh` (ask the owner first; it replaces the live app). The detailed machine-specific notes live in the ignored `local/` folder.
- Bump `packaging/home-assistant/version.txt` (`0.13.0+bookmeet.N`) with every push that will be deployed: it is stamped into the binary and shown in small type at the foot of every public page, so the owner can see which build is live.
- The Home Assistant updater currently consumes an exported customization patch. Regenerate it from this fork with `packaging/home-assistant/package.py`; do not maintain a second hand-edited source tree.
