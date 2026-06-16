# plan-only

Plan-only run via `client.RunPlan(...)`: classify (or accept caller `Steps`) and
print the structured checklist from `result.Plan` without executing the agent
loop.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

go run .

# optional custom prompt:
go run . "Roll out v2.4 to staging and prod with smoke tests in between"
```

To copy this example out of the monorepo, delete the `replace` directive from
`go.mod` and run `go get github.com/mantyx-io/mantyx-sdk/go@latest`.
