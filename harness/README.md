# Bloud Browser Harness

The shared browser code for Bloud. One npm package, `@bloud/browser-harness`,
holds everything that knows how to drive the Bloud UI in a browser: the
Authentik sign-in, the app origins, the dashboard locators, the host-agent API
client, the page objects, and the page diagnostics that make a failure
readable.

Two consumers use it:

- the **e2e suite** (`e2e/`) runs the behavior ladders through it, and
- an **agent recipe** (`harness/bin/browser.mjs`) looks at a page, takes a
  screenshot, or checks a page for problems, without a spec.

Because both import the same module, an ad-hoc look and the gate suite cannot
drift apart about what a selector means. See
[docs/guides/browser-harness.md](../docs/guides/browser-harness.md) for the
design and the full command reference.

## Layout

| Path | What it is |
|---|---|
| `src/` | The library. Type-only import of `@playwright/test`, no run-time Playwright. |
| `src/index.ts` | The public surface. |
| `cli/` | The recipe runner: a Playwright config and `recipe.spec.ts`. |
| `bin/browser.mjs` | The agent entry. It sets the recipe in the environment and runs `cli/`. |

## Install

```bash
npm --prefix harness ci                    # or: npm --prefix harness install
npm --prefix harness run install-browsers  # chromium, shared with the e2e cache
```

The harness owns its install because `bin/browser.mjs` runs its own Playwright.
The e2e suite does **not** need this install: it links the library from its own
`node_modules`, and the library loads no Playwright at run time.

## Use the library

```ts
import { ensureSignedIn, appOrigin, LoginPage, HarnessSession } from '@bloud/browser-harness';
```

The library observes and acts. It reports a problem by throwing an `Error` with
a readable message. It never uses the Playwright `expect` surface, so the
caller decides how to assert.

## Use the CLI

```bash
node harness/bin/browser.mjs shot jellyfin          # screenshot an app
node harness/bin/browser.mjs shot bloud --full      # full dashboard shot
node harness/bin/browser.mjs review immich          # url, title, text, problems
node harness/bin/browser.mjs verify jellyfin        # exit nonzero on problems
node harness/bin/browser.mjs review https://x --anon
```

Run `node harness/bin/browser.mjs --help` for the options.

Screenshots land in `.bloud/screenshots/` unless `--out` names another
directory.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `BLOUD_URL` | `http://localhost:8080` | The public origin (Traefik). |
| `BLOUD_APP_BASE_URL` | `BLOUD_URL` | Overrides the app subdomains only. |
| `BLOUD_API_URL` | `http://localhost:3000` | The host-agent API. |
| `BLOUD_E2E_USERNAME` | `admin` | The sign-in user. |
| `BLOUD_E2E_PASSWORD` | `password` | The sign-in password. |
