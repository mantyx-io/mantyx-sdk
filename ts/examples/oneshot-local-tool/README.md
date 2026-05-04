# oneshot-local-tool

A one-shot agent run that ships a single local tool (`read_file`). The MANTYX
runtime calls the local tool from your process, then continues the agent loop
on the server with the tool's output.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

npm install
npm start
```

The example depends on the SDK via a local path (`"@mantyx/sdk": "file:../.."`).
If you copy this directory out of the monorepo, replace that with the published
version (e.g. `"^0.1.0"`) before running `npm install`.
