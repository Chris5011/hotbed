# Contributing — dev guidelines for hotbed / Hotbed

Rules of the road for anything that lands in this repo, including by agents.
Goal: a linear, self-describing history that reads like a changelog — so a
public repo and the release playbook (`docs/RELEASING.md`) both stay clean.

## TL;DR

1. **Conventional Commits, always.** Every commit title is machine-parseable.
2. **One intent = one `type/<topic>` branch = one PR.** Never commit directly
   to `main` except trivial docs/readme fixes and release bookkeeping.
3. **Squash-merge into `main`.** The PR's title becomes the commit — so the PR
   title *must itself be* a valid conventional commit message.
4. `main` is always releasable: suite green, `make check` green.

## Conventional Commits

```
<type>(<optional scope>): <imperative summary, ≤72 chars, no trailing period>

<optional body: what and why, wrapped at ~72 columns>

<optional footers: sandbox/engine names, tickets, BREAKING CHANGE>
```

| type | use for | examples from this codebase |
|---|---|---|
| `feat` | new capability | `feat(api): VM create wizard for libvirt and docker backends` |
| `fix` | broken behavior | `fix(engine): keep shut-off libvirt domains in inventory` |
| `docs` | prose only | `docs: v0.2.1 release notes` |
| `refactor` | no behavior change | `refactor(engine): split VMCreate timeout plumbing` |
| `test` | suite only | `test(suite): boot-callback endpoint checks on 7794` |
| `chore` | maintenance | `chore(makefile): add release target` |
| `perf`, `build`, `ci`, `revert` | as they mean | `build(go): stamp Version via ldflags` |

Rules:
- **Imperative mood** ("add", not "added"): the title completes *"this commit
  will …"*.
- **Scope** names a module when it helps: `api`, `engine`, `dashboard`,
  `collector`, `suite`, `kit`, `vm`. Skip it when obvious.
- Reference artifacts in the **body**, not the title: sandbox/VM names, ports,
  audit traces, issue refs (`#12`).
- Breaking changes: `!` after the type (`feat(api)!:`) plus a
  `BREAKING CHANGE: <what must be migrated>` footer.
- Do **not** mix intents (a feat with reformatting, a fix with tests for other
  fixes). Split them.

### Examples

```
# good
feat(dashboard): first-load spinner with slow-scan hints
fix(api): parse SSE JSON payload before appending to logbox

# bad — vague, non-imperative, mixes concerns
"misc updates and fixes"
"fixed spinner and also improved logs + some vm stuff"
```

## Branches

- Branch off the current `main`; name by the dominant type + topic:
  `fix/collector-shutoff-vms`, `feat/dashboard-pending-actions`,
  `docs/releasing-playbook`, `chore/release-0.3.0`.
- **Short-lived**: hours-to-days. Rebase onto `main` right before opening the
  PR (`git fetch && git rebase origin/main`).
- A branch that balloons past ~8 commits is a smell — split it.
- WIP never goes to `main`; push drafts as draft-PRs if you need backup.

## PRs and merges

- **PR title = the conventional commit subject** (this is what makes the
  squashed history read like a changelog). Body = what/why + how-verified.
- **Squash-merge**, then delete the branch. No merge bubbles, no
  "Merge branch '…'" noise.
- Before opening a PR, locally:
  1. `make check` (gofmt guard, `go vet`, build)
  2. `bash -n vm/hotbedvm.sh && bash -n tests/hotbedddsmoke.sh`
  3. dashboard JS check — snippet in `docs/RELEASING.md` §2
  4. full suite via the §4 certification dance → `N passed, 0 failed`
- Never stage runtime artifacts (`.gitignore` covers them, but verify:
  `git status` must show no `vm/images`, `vm/vms`, `dist/`, `hotbed/bin`).
- Solo dev is not an excuse: open the PR anyway — it is your review record.

## Once public

- Protect `main` (require PRs, linear history, no force-push).
- Same rules apply to agents making commits on a user's behalf — they inherit
  these guidelines, and stop for approval when a change would break any of the
  `docs/RELEASING.md` hard no-lists (user sandboxes, foreign VMs, daemon PIDs).

## Releases

- Tags `vX.Y.Z` are cut **on `main` only**, never from feature branches.
  Full flow: `docs/RELEASING.md` §1–§7 (version bump locations, suite gate,
  `make release`, release-notes file, `gh release create`).
- Hotfix for a shipped tag: `fix/…` branch → PR → squash into `main` → bump
  version → retag per the playbook.
