# plan-only

Plan-only run via `client.run_plan(...)`: classify (or accept caller `steps`) and
print the structured checklist from `result.plan` without executing the agent
loop.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

uv run python main.py

# optional custom prompt:
uv run python main.py "Roll out v2.4 to staging and prod with smoke tests in between"
```

When developing inside the monorepo, `pyproject.toml` resolves `mantyx-sdk`
against `../..`. After copying out, pin the published package version instead.
