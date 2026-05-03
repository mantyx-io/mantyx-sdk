# Contributing to `mantyx-sdk` (Python)

Thanks for considering a contribution! This SDK is a small, dependency-light client for the [MANTYX](https://mantyx.com) agent-runs HTTP/SSE protocol; the goal is to keep it that way.

See the [top-level CONTRIBUTING.md](../CONTRIBUTING.md) for cross-cutting expectations (Conventional Commits, lockstep versioning).

## Ground rules

1. **Public-protocol only.** The SDK MUST NOT depend on any MANTYX-internal package, type, or repository layout. Anything it does is a side effect of sending HTTP requests against the public protocol documented in [`docs/agent-runs-protocol.md`](./docs/agent-runs-protocol.md).
2. **Tiny dep tree.** The runtime deps today are `httpx` and `pydantic` (v2). Adding a new runtime dependency requires a strong justification.
3. **Standalone tests.** `pytest` must pass with the local `pip install -e ".[dev]"` set, with no MANTYX server running.
4. **Sync ⇄ async parity.** Every feature on `MantyxClient` should land on `AsyncMantyxClient` in the same PR (and vice-versa). Tests cover both.
5. **Backwards compatibility.** Bump per [SemVer](https://semver.org/spec/v2.0.0.html). Breaking changes need a major version bump and a `CHANGELOG.md` entry.

## Local setup

```bash
cd python
python -m venv .venv
. .venv/bin/activate
python -m pip install -e ".[dev]"

pytest -q
ruff check .
ruff format --check .
mypy src
```

Min Python target is **3.9** (older end-of-life). Tests run against 3.9, 3.10, 3.11, 3.12, and 3.13 in CI.

## Adding tests

Tests live under [`tests/`](./tests). Network calls are routed through the in-process mock server in [`tests/conftest.py`](./tests/conftest.py) — extend it when you need to exercise a new server behaviour rather than reaching out to a real MANTYX instance. The mock matches the contract specified by [`docs/agent-runs-protocol.md`](./docs/agent-runs-protocol.md).

When fixing a bug, prefer a regression test against the mock server. When adding a feature, add an example under [`examples/`](./examples) that exercises it end-to-end.

## Style

- `ruff` for lint + format. `mypy --strict` for type-check.
- Public types are re-exported from [`src/mantyx/__init__.py`](./src/mantyx/__init__.py). Keep the public surface minimal and well-typed.
- Use `MantyxError` (and its subclasses) for raised errors so callers can branch on `isinstance`.
- Prefer `httpx` async APIs over Python's stdlib HTTP modules so the SDK keeps working under uvloop / Trio (`anyio`) if needed later.

## Releasing

Releases happen via the lockstep flow described in the [top-level CONTRIBUTING.md](../CONTRIBUTING.md). Briefly:

1. Bump the root `VERSION` and run `node scripts/sync-version.mjs`. This rewrites `python/sdk-version.txt` and `python/src/mantyx/_version.py`.
2. Run `node scripts/changelog.mjs --write` to regenerate `python/CHANGELOG.md` from the git log.
3. Commit, push to `main`, and trigger the **Publish** workflow from the Actions tab.

The publish workflow:

- Builds the wheel + sdist with `python -m build` from `python/`.
- Uploads to PyPI via [Trusted Publishing](https://docs.pypi.org/trusted-publishers/) (OIDC) — there is no PyPI API token in repo secrets.
- Pushes a `python/v$V` git tag.
- Opens a GitHub Release populated by `scripts/changelog.mjs --release-body`.

### One-time PyPI Trusted Publisher setup

The PyPI project `mantyx-sdk` must have a Trusted Publisher configured for this repo + workflow before the first release. See https://docs.pypi.org/trusted-publishers/adding-a-publisher/ — values:

| Field             | Value                          |
| ----------------- | ------------------------------ |
| Owner             | `mantyx`                       |
| Repository name   | `mantyx-sdk`                   |
| Workflow filename | `publish.yml`                  |
| Environment       | `pypi` (matches `publish.yml`) |

## Code of Conduct

Be kind. Assume good intent. Be patient with reviewers and maintainers.
