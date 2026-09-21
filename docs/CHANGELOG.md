# Changelog

All notable changes to this project. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
this project does not yet use semver — every entry sits under `Unreleased` until
the first tagged release.

## [Unreleased]

### Added
- `LICENSE` (MIT).
- `docs/` folder with `CHANGELOG.md`, `DECISIONS.md`, and an index.

### Changed
- Bumped CI action versions to Node 24: `actions/checkout@v5`,
  `actions/setup-go@v6`. Fixed the `on: push` trigger to include the `master`
  branch (was `main` only).

### Removed
- Dead-code helper `stripPrefix` and the now-unused `strings` import in
  `api/handlers.go`.
- The `lint` job from the CI workflow and the accompanying `.golangci.yml`
  config. Lint was churning on the golangci-lint v1 → v2 migration and
  costing more than it caught; may return once we adopt a version we can pin
  reliably. `go vet` still runs in CI as a basic static-analysis floor.
