# Changelog

All notable changes to this project. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
this project does not yet use semver — every entry sits under `Unreleased` until
the first tagged release.

## [Unreleased]

### Added
- `LICENSE` (MIT).
- `docs/` folder with `CHANGELOG.md`, `DECISIONS.md`, and an index.
- README badges: CI status, Go version (auto-read from `go.mod`), MIT license,
  Redis version.
- `.editorconfig` matching the styles already in the repo (2-space default,
  tabs for Go and Makefiles, 4-space for Lua, preserve trailing whitespace in
  Markdown for hard line breaks).
- Extended `.gitignore`: `dist/`, `*.prof`, `*.pprof`, `*.log`, `tmp/`, `.env`,
  `.env.local`, `.env.*.local`, `Thumbs.db`. Also grouped existing entries
  under section headers.

### Changed
- Bumped CI action versions to Node 24: `actions/checkout@v5`,
  `actions/setup-go@v6`. Fixed the `on: push` trigger to include the `master`
  branch (was `main` only).
- Replaced hand-rolled numeric helpers (`parseFloat`, `parseInt`, `formatFloat`,
  `formatInt`, `padTwo`) in `internal/store/fake.go` with `strconv` equivalents.
  Net: 72 lines deleted, 8 added. Behavior identical; token-state strings may
  now render without trailing `.00` (`strconv.FormatFloat` with `-1` precision
  emits the shortest round-trippable representation) — read-side is unchanged
  because both formats parse the same way.
- Split `api/handlers.go` (329 LOC) by concern into three files:
  `api/handlers.go` for the Handler struct + Routes + endpoint handlers,
  `api/auth.go` for `AdminAuth` / `BearerTokenAuth`, and `api/observe.go` for
  the HTTP observability middleware (`observe`, `requireAdmin`,
  `statusRecorder`, `statusClass`). No behavior change; imports and exports
  unchanged. See `docs/DECISIONS.md`.
- Added *why*-comments to non-obvious code paths: the fail-mode-to-metrics
  translation in `middleware.evaluate`, the health-probe 500 ms timeout, and
  the fixed-window inspection limitation on `/status`.
- Extracted `decisionLabel(bool) string` in `internal/middleware/ratelimit.go`
  so the happy-path and fail-mode paths can't drift on the Prometheus label
  string (`"allowed"` vs `"allow"` would silently split a counter).
- `Fake.HGetAll` uses `maps.Copy` (Go 1.21+ stdlib) instead of a manual
  for-range copy. Added a doc comment noting the defensive-copy contract.
- Corrected a misleading comment in `handlers.inspect` for the fixed-window
  branch — the previous version claimed the code "exposes the prefix"
  when in fact it returned a fixed note string.

### Removed
- Dead-code helper `stripPrefix` and the now-unused `strings` import in
  `api/handlers.go`.
- The `lint` job from the CI workflow and the accompanying `.golangci.yml`
  config. Lint was churning on the golangci-lint v1 → v2 migration and
  costing more than it caught; may return once we adopt a version we can pin
  reliably. `go vet` still runs in CI as a basic static-analysis floor.
