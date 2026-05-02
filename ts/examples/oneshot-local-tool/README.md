# oneshot-local-tool

A one-shot agent run that ships a single local tool (`read_file`). The MANTYX
runtime calls the local tool from your process, then continues the agent loop
on the server with the tool's output.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

pnpm install
pnpm start
```

Once published, replace `"@mantyx/sdk": "workspace:*"` in `package.json` with
the latest release version (e.g. `"^0.1.0"`) and run `npm install && npm start`.
