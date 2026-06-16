# plan-only

Plan-only run via `client.runPlan(...)`: classify (or accept caller `steps`) and
print the structured checklist from `result.plan` without executing the agent
loop.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

npm install
npm start

# optional custom prompt:
npm start -- "Roll out v2.4 to staging and prod with smoke tests in between"
```

The example depends on the SDK via a local path (`"@mantyx/sdk": "file:../.."`).
If you copy this directory out of the monorepo, replace that with the published
version before running `npm install`.
