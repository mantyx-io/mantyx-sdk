---
title: Authentication
description: Bearer credentials accepted by the SDK — workspace API keys and OAuth 2.0 access tokens.
sidebar:
  order: 2
---

Every call requires a single bearer credential. The SDK accepts two
interchangeable kinds:

- A **workspace API key** (token prefix `mantyx_`) with usage
  `developer_api`. Static, workspace-scoped, no expiry.
- A **MANTYX OAuth 2.0 access token** (token prefix `mantyx_at_`),
  user- and workspace-scoped, short-lived, and gated by per-route
  **scopes** (e.g. `runs:read`, `sessions:write`, `models:read`). Calls
  missing a required scope return `403 insufficient_scope`, surfaced
  as a typed scope error so callers can drive a re-consent flow.

Both flow through `Authorization: Bearer <token>` and are resolved by
the server purely from the token prefix. The SDK constructor exposes
them as mutually exclusive options — pass exactly one of `apiKey`,
`accessToken`, or `tokenSource`.

## Workspace API key

1. Open the MANTYX dashboard.
2. Go to **Settings → API keys**.
3. Click **Create key**, scope it to the right workspace and
   (optionally) restrict the `agentIds` allowlist to the persisted
   agents you want this key to be able to trigger. An empty allowlist
   means "any agent in the workspace".
4. Copy the key. The dashboard only shows it once.

```ts
import { MantyxClient } from "@mantyx/sdk";

const client = new MantyxClient({
  apiKey: process.env.MANTYX_API_KEY!,        // mantyx_…
  workspaceSlug: "acme-corp",
});
```

The SDKs send the key as `Authorization: Bearer <key>` (you can also
use `X-API-Key: <key>` if you call the HTTP API directly).

## OAuth 2.0 access token

For apps that other people sign in to (multi-tenant SaaS, end-user
clients, service-to-service inside a customer's account), register an
OAuth application in the MANTYX dashboard and run the
Authorization Code + PKCE flow (or `client_credentials` for private
machine-to-machine apps).

The **fastest path** is to let the SDK's built-in OAuth client refresh
tokens for you. Refresh tokens are persistent and non-rotating — you
store them once at first sign-in.

```ts
import { MantyxClient, MantyxOAuthClient } from "@mantyx/sdk";

const oauth = new MantyxOAuthClient({
  clientId: process.env.MANTYX_OAUTH_CLIENT_ID!,        // mantyx_oa_…
  clientSecret: process.env.MANTYX_OAUTH_CLIENT_SECRET!, // mantyx_oas_…
});

const client = new MantyxClient({
  tokenSource: oauth.refreshTokenSource({
    refreshToken: storedRefreshToken,       // mantyx_rt_…
  }),
  workspaceSlug: "acme-corp",
});
```

If you already have a short-lived access token managed elsewhere, pass
it directly via `accessToken` (TS / Python) or `AccessToken` (Go); the
SDK will not refresh on your behalf. See [OAuth 2.0](/docs/oauth/) for
the full grant matrix, scope catalog, and token-format reference, and
each SDK README's "OAuth 2.0 refresh" subsection for the call-site
shapes in Python and Go.

## Required environment

The examples in this site assume two environment variables:

```bash
export MANTYX_API_KEY=mantyx_...
export MANTYX_WORKSPACE_SLUG=acme-corp
```

The workspace slug is the URL component you see in the dashboard
(e.g. `https://app.mantyx.com/acme-corp`). The slug your credential
is scoped to must match the slug you pass to the SDK constructor;
mismatches return `404 not_found`.

## Self-hosted MANTYX

Override the base URL when constructing the client:

```ts
const client = new MantyxClient({
  apiKey: process.env.MANTYX_API_KEY!,
  workspaceSlug: "acme-corp",
  baseUrl: "https://mantyx.internal.acme.com",
});
```

(Equivalent options exist on the [Go](/docs/reference/go/) and
[Python](/docs/reference/python/) clients. For OAuth, pass the matching
`baseUrl` to `MantyxOAuthClient` so the SDK talks to the right
authorization server.)
