# MANTYX SDK — landing & docs site

Astro Starlight site that ships:

- A custom marketing landing page (`/`) — see [`src/content/docs/index.mdx`](./src/content/docs/index.mdx) and the components under [`src/components/`](./src/components/).
- The full SDK documentation under `/docs/...` — see [`src/content/docs/docs/`](./src/content/docs/docs/).

The site is published to GitHub Pages by [`.github/workflows/docs.yml`](../.github/workflows/docs.yml) on every push to `main`.

## Local dev

```bash
cd site
npm install
npm run dev          # http://localhost:4321/mantyx-sdk/
npm run build        # static output under site/dist/
npm run preview
```

The base path is `/mantyx-sdk/` by default (assumes a project page at `https://<org>.github.io/mantyx-sdk/`). Override with the `SITE_BASE` env var:

```bash
# Custom domain → root
SITE_BASE=/ npm run dev

# Different repo name
SITE_BASE=/some-other-name npm run dev
```

## One-time GitHub Pages setup

After merging this repo's `docs.yml` workflow once:

1. Open **Repo → Settings → Pages**.
2. Set **Source** to **GitHub Actions**.
3. Re-run the `Docs` workflow from the **Actions** tab if needed.

Subsequent merges to `main` redeploy automatically.

## Where the docs content comes from

To avoid duplication, several pages are imported from existing markdown sources rather than copy-pasted:

- `/docs/protocol/` reads [`docs/agent-runs-protocol.md`](../docs/agent-runs-protocol.md) at build time.
- The reference pages link out to the per-SDK READMEs under [`ts/`](../ts), [`go/`](../go), and [`python/`](../python).

The single source of truth for the wire protocol is the protocol doc; for SDK behaviour it is each SDK's README. Edit those files; the site picks the changes up automatically.
