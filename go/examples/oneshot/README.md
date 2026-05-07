# oneshot

Run a single agent turn with one local tool (`read_file`).

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

go run .
```

To copy this example out of the monorepo, delete the `replace` directive from
`go.mod` and run `go get github.com/mantyx-io/mantyx-sdk/go@latest`.
