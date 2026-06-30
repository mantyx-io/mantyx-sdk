# Contributing to MANTYX SDKs

Thanks for your interest in contributing! This monorepo ships three first-party SDKs:

| SDK | Source | Package | Contributing notes |
| --- | --- | --- | --- |
| TypeScript | [`ts/`](./ts) | `@mantyx/sdk` (npm) | [`ts/CONTRIBUTING.md`](./ts/CONTRIBUTING.md) |
| Go | [`go/`](./go) | `github.com/mantyx-io/mantyx-sdk/go` | [`go/CONTRIBUTING.md`](./go/CONTRIBUTING.md) |
| Python | [`python/`](./python) | `mantyx-sdk` (PyPI) | [`python/CONTRIBUTING.md`](./python/CONTRIBUTING.md) |

…plus the docs site at [`site/`](./site) (Astro Starlight, deployed to GitHub Pages).

## Conventional Commits (required)

Commit messages must follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <subject>

[body]

[BREAKING CHANGE: <reason>]
```

This is what feeds [`scripts/changelog.mjs`](./scripts/changelog.mjs) at release time — the per-SDK `CHANGELOG.md` files are regenerated from git history when you run the **Publish** workflow (not on every PR). If your commit subject doesn't follow this format, your change won't appear in any `CHANGELOG.md` or release notes.

### Recognised types

| Type       | Goes to CHANGELOG section | Notes |
| ---------- | ------------------------- | ----- |
| `feat`     | **Added**                 | New user-visible feature |
| `fix`      | **Fixed**                 | Bug fix |
| `perf`     | **Performance**           | Performance improvement |
| `refactor` | **Changed**               | Internal refactor with user-visible side effects |
| `docs`     | **Documentation**         | Doc-only changes |
| `style`    | _skipped_                 | Whitespace / formatting |
| `test`     | _skipped_                 | Test-only changes |
| `chore`    | _skipped_                 | Maintenance |
| `ci`       | _skipped_                 | CI changes |
| `build`    | _skipped_                 | Build pipeline changes |

A `BREAKING CHANGE:` footer (or a `!` after the type/scope, e.g. `feat(ts)!: drop Node 16`) lands the commit under a **Breaking** section regardless of type.

### Recognised scopes

Use a scope so the entry lands in the right SDK's `CHANGELOG.md`:

| Scope        | Routes to                                        |
| ------------ | ------------------------------------------------ |
| `ts`         | `ts/CHANGELOG.md`                                |
| `go`         | `go/CHANGELOG.md`                                |
| `py`         | `python/CHANGELOG.md`                            |
| `protocol`   | All three (changes to the wire spec)             |
| `docs`       | All three (`docs/**` shared content)             |
| `site`       | The marketing/docs site only (no SDK CHANGELOG)  |
| `ci` / `build` | Skipped from public CHANGELOGs                 |
| `repo`       | All three (cross-cutting tooling at the root)    |

### Examples

```
feat(ts): add streamAgent retry on transient 5xx
fix(go): handle SSE reconnect when Last-Event-ID is stale
feat(py): initial release
docs(protocol): clarify local_tool_call schema
chore(site): bump astro to 5.18
ci: cache pip wheels in the python job
```

## Releasing

The repo uses **lockstep versioning** across all three SDKs. The root [`VERSION`](./VERSION) file is the single source of truth; the **Publish** workflow bumps it, syncs downstream files, and commits the result — you do not need to edit `VERSION` or run release scripts locally.

### One-click release

1. Open **Actions → Publish** on `main`.
2. Choose **patch**, **minor**, or **major** (or set an explicit **version** to override the bump).
3. Optionally enable **dry_run** first to verify builds without publishing.
4. Click **Run workflow**.

The workflow will:

1. Bump [`VERSION`](./VERSION) and run [`scripts/sync-version.mjs`](./scripts/sync-version.mjs) + [`scripts/changelog.mjs`](./scripts/changelog.mjs).
2. Run all three test suites.
3. Publish to npm + PyPI and push git tags `v<version>`, `go/v<version>`, and `python/v<version>`.
4. Commit version files and final `CHANGELOG.md` files to `main` (`chore(repo): release v…`).
5. Open a GitHub Release per tag, with notes from `scripts/changelog.mjs --release-body`.

**Prerequisites (once per repo):** `NPM_TOKEN` secret; PyPI Trusted Publisher — see [`python/CONTRIBUTING.md`](./python/CONTRIBUTING.md).

### If something goes wrong

| Failure point | State | Recovery |
| ------------- | ----- | -------- |
| Fails before publish | Nothing shipped | Re-run the workflow |
| npm/PyPI published, commit push fails | Packages live, `main` stale | Manually commit version/CHANGELOG files, or re-run with the same **version** (tag guard blocks re-tag; delete tags first if needed) |
| `dry_run` | No side effects | Safe to run anytime |

Do **not** edit `CHANGELOG.md` by hand or regenerate it in PRs — the Publish workflow updates all three files when a release ships.

## Agent-runs protocol document

The wire spec lives once at [`docs/agent-runs-protocol.md`](./docs/agent-runs-protocol.md). The same file is **copied** (not edited separately) to:

- [`go/docs/agent-runs-protocol.md`](./go/docs/agent-runs-protocol.md)
- [`python/docs/agent-runs-protocol.md`](./python/docs/agent-runs-protocol.md)
- [`ts/docs/agent-runs-protocol.md`](./ts/docs/agent-runs-protocol.md)

After changing the canonical file, refresh the mirrors:

```bash
node scripts/sync-agent-runs-doc.mjs
git add docs go/docs python/docs ts/docs
```

CI runs `node scripts/sync-agent-runs-doc.mjs --check` so drift fails the build.

## Git hooks (optional)

CI drift usually happens when only **half** of a mechanical fix is committed (for example editing `docs/agent-runs-protocol.md` without syncing mirrors). To format staged Python files and run the same drift checks locally before each commit, enable hooks once per clone:

```bash
./scripts/setup-git-hooks.sh
```

That runs [`githooks/pre-commit`](./githooks/pre-commit) — auto-format via [`scripts/format.sh`](./scripts/format.sh), then version-sync and protocol-doc checks (see [`githooks/README.md`](./githooks/README.md)).

Format the Python tree manually:

```bash
./scripts/format.sh
```

For per-SDK setup (Node, Go, Python), see the per-package contributing guide linked in the table above.

## Pull requests

- Keep PRs scoped to a single SDK or shared concern when possible — it keeps the per-SDK CHANGELOGs clean.
- Add tests covering the behaviour you're changing (every SDK has a mock-server test suite to model new wire interactions on).
- The CI workflow in [`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs all three SDK suites and the protocol-doc mirror check on every PR.

## License

By contributing, you agree your contributions are licensed under [Apache-2.0](./ts/LICENSE).
