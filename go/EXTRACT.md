# Extracting the Go module into its own repository

The Go module under this folder (`github.com/mantyx-io/mantyx-sdk/go`) is intentionally self-contained. Lifting it
out of the MANTYX SDK monorepo into a public repository is a five-minute job:

## Steps

1. Copy this folder verbatim:

   ```bash
   cp -r /path/to/mantyx-sdk/go ~/code/mantyx-sdk-go
   cd ~/code/mantyx-sdk-go
   ```

2. Initialize git:

   ```bash
   git init -b main
   git add .
   git commit -m "Import Go SDK from mantyx-sdk monorepo"
   ```

3. Run `go mod tidy` to refresh the lockfile:

   ```bash
   go mod tidy
   ```

4. For each example, remove the `replace` directive that points at the
   monorepo and refresh:

   ```bash
   for d in examples/*/; do
     pushd "$d" >/dev/null
     # remove the `replace github.com/mantyx-io/mantyx-sdk/go => ../..` line
     go mod edit -dropreplace github.com/mantyx-io/mantyx-sdk/go
     go mod tidy
     popd >/dev/null
   done
   ```

5. Confirm the test suite still passes:

   ```bash
   go test ./...
   go vet ./...
   go build ./...
   ```

6. Push to a new GitHub repo. To enable Go module discovery, ensure the new
   repo path matches the module declaration in `go.mod` (default:
   `github.com/mantyx-io/mantyx-sdk/go`). Update `go.mod` if you publish under
   a different path.

## What you can leave behind

The following monorepo-only files do **not** apply to a standalone repo:

- The MANTYX monorepo's root `pnpm-workspace.yaml`, `AGENTS.md`, `run.sh`,
  `infra/`, `mobile/`, etc. None of them are required by the SDK.

## Things to update once extracted

- `go.mod` `module` line if you publish under a different path.
- `README.md` install instructions if the import path changes.
- The `defaultBaseURL` constant in `client.go` if you intend to talk to a
  non-default MANTYX deployment.

## What stays the same

- The wire protocol the SDK speaks (`docs/agent-runs-protocol.md`).
- The `Client` API surface.
- The example projects (after the `replace` edit above).
- Tests and the mock server (zero MANTYX dependencies).

That's it. The extracted module can be built, tested, and tagged from any
environment with Go 1.22+ installed.
