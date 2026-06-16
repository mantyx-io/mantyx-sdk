# Contributing to `@mantyx/sdk`

Thanks for considering a contribution! This SDK is a small, dependency-light
client for the [MANTYX](https://mantyx.com) agent-runs HTTP/SSE protocol; the
goal is to keep it that way.

## Ground rules

1. **Public-protocol only.** The SDK MUST NOT depend on any MANTYX-internal
   package, type, or repository layout. Anything it does is a side effect of
   sending HTTP requests against the public protocol documented in
   [`docs/agent-runs-protocol.md`](./docs/agent-runs-protocol.md).
2. **No `workspace:*`.** The SDK ships to npm as a standalone package. Pull
   requests that introduce a `workspace:*` dependency in `package.json` will
   not be merged.
3. **Tiny dep tree.** The only runtime dependency today is `zod`. Adding a
   new runtime dependency requires a strong justification.
4. **Standalone tests.** `pnpm test` must pass with `node_modules/` populated
   from this package's own `package.json`, with no MANTYX server running.
5. **Backwards compatibility.** Bump the SDK version per
   [SemVer](https://semver.org/spec/v2.0.0.html). Breaking changes need a
   major version bump and a `CHANGELOG.md` entry.

## Local setup

```bash
pnpm install
pnpm test
pnpm typecheck
pnpm build
```

The repository is a pnpm monorepo, but this package is a leaf with no
internal consumers, so it is safe to develop in isolation.

## Adding tests

Tests live under [`test/`](./test) and use Vitest. Network calls are routed
through the in-process mock server in
[`test/helpers/mock-server.ts`](./test/helpers/mock-server.ts) — extend it
when you need to exercise a new server behaviour rather than reaching out to
a real MANTYX instance.

When fixing a bug, prefer a regression test against the mock server. When
adding a feature, add an example under [`examples/`](./examples) that
exercises it end-to-end.

## Style

- TypeScript strict mode. No `any` unless there's no choice.
- Public types are re-exported from [`src/index.ts`](./src/index.ts). Keep
  the public surface minimal and well-typed.
- Use `MantyxError` (and its subclasses) for thrown errors so callers can
  branch on `instanceof`.
- Prefer the Web `fetch` / `ReadableStream` APIs over Node-only modules so
  the SDK keeps working in edge / browser-like runtimes.

## Pull requests

1. Fork and create a feature branch.
2. Add or update tests.
3. `pnpm test && pnpm typecheck && pnpm build`.
4. Use [Conventional Commits](https://www.conventionalcommits.org/) with scope `ts` — `CHANGELOG.md` is regenerated from git history on release (do not edit it by hand).
5. Open a PR. Describe the change and any protocol implications.

## Releasing

Releases are handled by the shared **Publish** GitHub Actions workflow on `main` (lockstep across all three SDKs). See the [top-level CONTRIBUTING.md](../CONTRIBUTING.md#releasing). The npm tag is `vX.Y.Z`; do not publish or tag manually from this package.

## Code of Conduct

Be kind. Assume good intent. Be patient with reviewers and maintainers.
