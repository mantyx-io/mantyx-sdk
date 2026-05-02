# Extracting `@mantyx/sdk` into its own repository

The `@mantyx/sdk` package is intentionally self-contained. Lifting it out of
the MANTYX monorepo into a public repository is a five-minute job:

## Steps

1. Copy this folder verbatim:

   ```bash
   cp -r packages/mantyx-sdk/ts ~/code/mantyx-sdk
   cd ~/code/mantyx-sdk
   ```

2. Initialize git:

   ```bash
   git init -b main
   git add .
   git commit -m "Import @mantyx/sdk from monorepo"
   ```

3. Re-generate the lockfile against npm:

   ```bash
   rm -rf node_modules
   pnpm install   # or: npm install
   ```

4. Update `examples/*/package.json` so each example installs `@mantyx/sdk`
   from npm rather than via the monorepo's pnpm workspace symlink. Each
   example already sets the dep to `"@mantyx/sdk": "*"`; replace `"*"` with
   the published version once you've cut a release.

5. Confirm the test suite still passes:

   ```bash
   pnpm test
   pnpm typecheck
   pnpm build
   ```

6. Push to a new GitHub repo and (optionally) wire up CI by copying
   `.github/workflows/sdk-typescript.yml` from the monorepo into
   `.github/workflows/ci.yml` in the new repo.

## What you can leave behind

The following monorepo-only files do **not** apply to a standalone repo:

- The MANTYX monorepo's root `pnpm-workspace.yaml`, `AGENTS.md`, `run.sh`,
  `infra/`, `mobile/`, etc. None of them are required by the SDK.

## Things to update once extracted

- `package.json` `repository`, `homepage`, and `bugs` URLs.
- `README.md` install instructions if the package name changes (e.g. you
  publish under a different scope).
- The `DEFAULT_BASE_URL` constant in `src/client.ts` if you intend to talk to
  a non-default MANTYX deployment.

## What stays the same

- The wire protocol the SDK speaks (`docs/agent-runs-protocol.md`).
- The `MantyxClient` API surface.
- The example projects.
- Tests and the mock server (zero MANTYX dependencies).

That's it. The extracted package can be built, tested, and published from
any environment with Node.js 18.17+ installed.
