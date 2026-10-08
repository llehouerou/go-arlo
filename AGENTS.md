# AGENTS.md

## Agent skills

### Issue tracker

Issues live in GitHub Issues on `llehouerou/go-arlo` (via `gh`). See `docs/agents/issue-tracker.md`.

### Triage labels

Default canonical labels: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `GLOSSARY.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.

## Releasing

A release is a semver tag on master plus its GitHub release:

1. `go vet ./... && go test -race ./...` pass.
2. Version: minor (`v0.N+1.0`) if the exported API grew or broke, else patch.
3. `git tag vX.Y.Z && git push origin master vX.Y.Z` (lightweight tag).
4. `gh release create vX.Y.Z --verify-tag --title vX.Y.Z --notes-file …`:
   sections New / Changed / Fixed / Docs / Internal from the commits since
   the previous tag, ending with whether the API grew, broke, or did not
   change. The repo is public: no names, account ids, serials or addresses.
5. Check the Go proxy serves it:
   `GOPROXY=https://proxy.golang.org go list -m github.com/llehouerou/go-arlo@vX.Y.Z`.
