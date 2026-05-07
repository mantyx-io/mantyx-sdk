# Contributing to the MANTYX Go SDK

Thanks for considering a contribution! This SDK is a small, dependency-light
Go client for the [MANTYX](https://mantyx.com) agent-runs HTTP/SSE protocol;
the goal is to keep it that way.

## Ground rules

1. **Public-protocol only.** The SDK MUST NOT depend on any MANTYX-internal
   package, type, or repository layout. Anything it does is a side effect of
   sending HTTP requests against the public protocol documented in
   [`docs/agent-runs-protocol.md`](./docs/agent-runs-protocol.md).
2. **Tiny dep tree.** `go.mod` should list only the standard library and
   `github.com/invopop/jsonschema`. Adding a new third-party dependency
   requires a strong justification.
3. **No CGO.** All builds must succeed with `CGO_ENABLED=0`.
4. **Standalone tests.** `go test ./...` must pass without any MANTYX server
   running.
5. **Backwards compatibility.** Bump the SDK version per
   [SemVer](https://semver.org/spec/v2.0.0.html). Breaking changes need a
   major version bump and a `CHANGELOG.md` entry.

## Local setup

```bash
go test ./...
go vet ./...
go build ./...
```

The repository lives at `packages/mantyx-sdk/go/` in the MANTYX monorepo,
alongside the TypeScript SDK at `packages/mantyx-sdk/ts/`. The Go module
itself has no internal consumers, so you can treat it as a standalone Go
module — the only repo-tie is the `replace` directive used by examples.

## Adding tests

Tests live alongside source files (`*_test.go`) and use the standard
`testing` package together with `net/http/httptest`. Network calls are routed
through the in-process mock server in
[`mock_server_test.go`](./mock_server_test.go) — extend it when you need to
exercise a new server behaviour rather than reaching out to a real MANTYX
instance.

When fixing a bug, prefer a regression test against the mock server. When
adding a feature, add an example under [`examples/`](./examples) that
exercises it end-to-end.

## Style

- Idiomatic Go. Prefer explicit error returns over panics.
- Follow `gofmt`. Run `go vet ./...` before pushing.
- Public symbols get godoc comments. Use the package-level overview in
  [`doc.go`](./doc.go) for cross-cutting docs.
- Use `*Error`, `*AuthError`, `*NetworkError`, `*RunError`, and `*ToolError`
  for thrown errors so callers can branch with `errors.As`.

## Pull requests

1. Fork and create a feature branch.
2. Add or update tests.
3. `go test ./... && go vet ./... && go build ./...`.
4. Update `CHANGELOG.md` under `## [Unreleased]`.
5. Open a PR. Describe the change and any protocol implications.

## Releasing

Releases happen out-of-band by a MANTYX maintainer:

1. Move `## [Unreleased]` content into a new version section in
   `CHANGELOG.md`.
2. Tag the commit `sdk-go-vX.Y.Z` and push. Go's module proxy picks it up
   automatically.
3. (Optional) Mirror the tag to a standalone GitHub repo if the SDK has been
   extracted.

## Code of Conduct

Be kind. Assume good intent. Be patient with reviewers and maintainers.
