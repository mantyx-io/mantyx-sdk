# Extracting `mantyx-sdk` into its own repository

The Python SDK is intentionally self-contained. Lifting it out of the MANTYX monorepo into a public repository is a five-minute job:

## Steps

1. Copy this folder verbatim:

   ```bash
   cp -R packages/mantyx-sdk/python ~/code/mantyx-sdk-python
   cd ~/code/mantyx-sdk-python
   ```

2. Initialise git:

   ```bash
   git init -b main
   git add .
   git commit -m "Import mantyx-sdk from monorepo"
   ```

3. Re-create a fresh virtualenv and run the test suite:

   ```bash
   python -m venv .venv
   . .venv/bin/activate
   python -m pip install -e ".[dev]"
   pytest -q
   ```

4. Update `examples/*/pyproject.toml` so each example resolves `mantyx-sdk` from PyPI rather than via the monorepo `path = "../.."` source. After the first PyPI release each example just needs:

   ```toml
   dependencies = ["mantyx-sdk>=0.1.0"]
   ```

5. Push to a new GitHub repo and (optionally) wire up CI by copying `.github/workflows/python.yml` from the monorepo into `.github/workflows/ci.yml` in the new repo.

## Things to update once extracted

- `pyproject.toml` `Source`/`Documentation`/`Issues` URLs.
- `README.md` install instructions if the package name changes.
- The `DEFAULT_BASE_URL` constant in `src/mantyx/client.py` if you intend to talk to a non-default MANTYX deployment.

## What stays the same

- The wire protocol the SDK speaks (`docs/agent-runs-protocol.md`).
- The `MantyxClient` / `AsyncMantyxClient` API surface.
- The example projects.
- Tests and the mock server (zero MANTYX dependencies).

## What you can leave behind

The following monorepo-only files do **not** apply to a standalone repo:

- The MANTYX monorepo's root tooling (`run.sh`, `infra/`, `mobile/`, etc.). None of them are required by the SDK.
- The repo-root `VERSION` + `scripts/sync-version.mjs` becomes `python/pyproject.toml`'s sole responsibility once extracted; you can simplify the version flow to a single bump-and-tag step.

That's it. The extracted package can be built, tested, and published from any environment with Python 3.9+ installed.
