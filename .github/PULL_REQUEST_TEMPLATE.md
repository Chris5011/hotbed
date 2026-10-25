<!-- The PR title becomes the squashed commit: use a conventional commit subject,
     e.g.  fix(api): honest 404 on double-delete  ·  see CONTRIBUTING.md -->

# What

<!-- one intent: what changed, and why (not how) -->

# How verified

- [ ] `make check` green (gofmt guard + `go vet` + build)
- [ ] `bash -n vm/sbxvm.sh && bash -n tests/sbxddsmoke.sh`
- [ ] dashboard JS: excerpted `appJS` passes `node --check` (snippet: docs/RELEASING.md §2)
- [ ] full suite via the §4 certification dance: `____ passed, 0 failed` (incl. `certsuite` cleanup)
- [ ] `git status` clean of runtime artifacts (vm/images, vm/vms, dist/, sbxd/bin)

# Screenshots / evidence

<!-- for dashboard changes: before/after; for engine changes: audit rows / ps output -->

# Notes for reviewers

<!-- risk areas, follow-ups, breaking changes -->
