---
title: Authentication
description: How to generate a workspace API key and authenticate the SDK.
sidebar:
  order: 2
---

Every call requires a workspace API key with usage `developer_api`.

1. Open the MANTYX dashboard.
2. Go to **Settings → API keys**.
3. Click **Create key**, scope it to the right workspace and (optionally) restrict the `agentIds` allowlist to the persisted agents you want this key to be able to trigger. An empty allowlist means "any agent in the workspace".
4. Copy the key. The dashboard only shows it once.

The SDKs send the key as `Authorization: Bearer <key>` (you can also use `X-API-Key: <key>` if you call the HTTP API directly).

## Required environment

The examples in this site assume two environment variables:

```bash
export MANTYX_API_KEY=mtx_live_...
export MANTYX_WORKSPACE_SLUG=acme-corp
```

The workspace slug is the URL component you see in the dashboard (e.g. `https://app.mantyx.com/acme-corp`). The slug in the `Authorization` header's tenant must match the slug you pass to the SDK constructor; mismatches return `404 not_found`.

## Self-hosted MANTYX

Override the base URL when constructing the client:

```ts
const client = new MantyxClient({
  apiKey: process.env.MANTYX_API_KEY!,
  workspaceSlug: "acme-corp",
  baseUrl: "https://mantyx.internal.acme.com",
});
```

(Equivalent options exist on the [Go](/docs/reference/go/) and [Python](/docs/reference/python/) clients.)
