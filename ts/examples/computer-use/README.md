# computer-use

A model-agnostic "computer use" agent: the SDK drives a screenshot -> action
loop where a **vision-capable model** looks at a screenshot of a browser and
calls built-in UI tools (`click_at`, `type_text_at`, `scroll_at`, …). Unlike a
vendor's dedicated computer-use model, this works with any vision model in your
workspace and keeps every wire-protocol feature (sessions, loop detection, tool
budgets, the supervisor judge, observability).

How it fits the wire protocol: tool results are string-only, so actions go out
as local-tool calls and screenshots come back as **user-message attachments** —
one fresh screenshot per step. `runComputerUse` owns the outer loop and stops
when the model replies with plain text (no tool call) or `maxSteps` is reached.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"
# Any vision-capable model id in your workspace:
export MANTYX_MODEL="google/gemini-3-flash"

npm install
npx playwright install chromium
npm start -- "Go to wikipedia.org and search for the Apollo program."
```

The example uses `PlaywrightController` from `@mantyx/sdk/playwright` — install `playwright`
alongside `@mantyx/sdk` in your own app the same way.

The example depends on the SDK via a local path (`"@mantyx/sdk": "file:../.."`).
If you copy this directory out of the monorepo, replace that with the published
version before running `npm install`.

## Safety

Computer use carries real risk (prompt injection from page content, unintended
clicks, scams). The example wires a terminal **human-in-the-loop** confirmation:
the model must call `request_confirmation` before consequential or irreversible
actions (purchases, sending messages, logging in, accepting terms, CAPTCHAs) and
the loop asks you on the command line. For production:

- Run in a sandboxed browser profile / VM / container.
- Add a URL allowlist/blocklist in your `BrowserController`.
- Log screenshots, proposed actions, and what was executed.

## Notes / limitations

- A general vision model grounds pixel coordinates less precisely than a model
  tuned for GUI control; expect occasional mis-clicks and keep `maxSteps`
  bounded.
- Screenshots are sent as inline JPEG (quality 70) to stay under the 5MB inline
  attachment cap. For large/retina viewports, downscale before sending.
- Cost/latency scale with steps: each step is one model turn plus one
  screenshot.
