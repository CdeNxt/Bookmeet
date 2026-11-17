# Developing Bookmeet with Claude Code

How work is organised, for the owner and for every agent session. Short on purpose.

## The layout

| Folder | Branch | Used for |
|---|---|---|
| `~/Developer/Bookmeet` | `bookmeet` | Review, merging, releases. Never feature work. |
| `~/Developer/Bookmeet-ai` | `booking-assistant-v0` | Long-running AI work. |
| `~/Developer/Bookmeet/.claude/worktrees/<task>` | `<kind>/<task>` | One short task each. Removed after merge. |

A worktree is a second folder backed by the same repository, on its own branch. Two sessions
in two worktrees cannot overwrite each other's files. Two sessions in one folder can.

`bookmeet` is protected on GitHub: changes arrive by pull request with passing checks.

Checks: `make check` (while working), `make test`, `make verify` (before a pull request; the
same as CI). Use `gh` with no `--repo`: its default is the fork, not upstream Calnode.

## A. New feature

1. Start a new Claude Code session with one objective and its acceptance criteria. Tick
   "worktree" when starting it in the desktop app, or ask the session to create one:
   `git worktree add -b feat/<name> .claude/worktrees/<name> origin/bookmeet`
2. The session reads the vault's `CONTEXT.md` and `TASKS.md`, then the notes for its area
   (table in `CLAUDE.md`).
3. Build, with tests. `make verify`.
4. Push, open a pull request to `bookmeet`. Fix what CI finds.
5. Merge when green. Write the handoff (E). Remove the worktree:
   `git worktree remove .claude/worktrees/<name>` and delete the branch.

## B. Long-running AI feature

Work in `~/Developer/Bookmeet-ai`. Its draft pull request to `bookmeet` runs CI on every push.

Keep it close to `bookmeet`. At the start of each session, and after anything merges there:

```sh
git fetch origin
git merge origin/bookmeet      # never rebase this branch: it is shared and deployed
make test
git push
```

Small merges often, not one large merge at the end. If a migration number collides, renumber
the AI branch's migration, never the one already on `bookmeet`.

Unfinished AI work reaches `bookmeet` only behind its switch (off by default) and with the
owner's yes. A finished slice can be split out: cut a branch from `origin/bookmeet`,
cherry-pick the slice, open its own pull request.

## C. Quick frontend improvement

Same as A, smaller. A fresh session in a temporary worktree, the one change, `make verify`
(it includes the visual tests), a screenshot at desktop and phone width in the pull request,
merge, remove the worktree. Do not reuse an old long chat for it.

## D. Parallel sessions

- One folder, one session. Check with `git worktree list` before starting.
- Two sessions must not edit the same files. If two tasks need the same file, run them one
  after the other.
- Only one branch deploys to staging at a time. Say in the session which.
- Before adding a migration: `git fetch` and look at the highest number on `origin/bookmeet`
  and on the AI branch.
- The vault has one writer at a time: a session writes its handoff once, when it finishes.
  If two sessions finish together, the second re-reads the file before appending.

## E. Session handoff

A new session resumes from the vault and the pull request, not from the old chat. At the end
of a session, append to the vault's `LOG.md`:

```
## 2026-11-16: <task> (<branch>, PR #<n>)
- Objective:
- Done:
- Tested:
- Decisions:
- Open issues:
- Next:
- Deployed: no | staging <version> | production <version>
```

One short line each. Then tick or add items in `TASKS.md`, and record a decision the owner
made in `DECISIONS.md`. Use `/compact` to continue the same task in a long session; start a
new session for a new deliverable. Old conversations are kept, never deleted.

## F. Staging and production release

1. Everything for the release is merged to `bookmeet` with green checks. `VERSION` is bumped.
2. In `~/Developer/Bookmeet`: `git pull`, then `packaging/fly/deploy.sh staging`.
3. Check the change at https://staging.bookmeet.date, on a phone as well where it matters.
4. Ask the owner. On their yes: `packaging/fly/deploy.sh production`. The script refuses a
   commit that is not on `origin/bookmeet` or that staging is not running, confirms the live
   version, and adds the release line to the vault's `LOG.md`.
5. Check https://bookmeet.date/version and the page you changed.

**Rolling back.** `fly releases --app bookmeet` lists releases; `fly deploy --image <previous
image> --app bookmeet` puts an earlier one back. This reverses code only. Migrations run at
start-up and are not undone: an older binary on a newer database works only if the migration
was additive (new table, new nullable column). So keep migrations additive, and for one that
is not, take a manual volume snapshot first (`fly volumes snapshots create <volume id>`) and
say so before deploying. The database is also copied continuously to Backblaze by Litestream.

Staging can run an unmerged branch for testing. That is fine; production cannot.

## Every substantial feature

Objective, acceptance criteria, who may do it (user, role, later plan), a few lines on agent
use (could an agent do this without the UI, what must the owner approve, how to measure it),
tests, `make verify`, docs, handoff. For a small fix, skip the ceremony and just do it well.
