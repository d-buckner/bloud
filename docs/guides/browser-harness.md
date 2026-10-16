# The browser harness

The browser harness is the one place that holds the knowledge of driving the
Bloud UI in a browser. It is an npm package, `@bloud/browser-harness`, in the
top-level `harness/` directory. The e2e suite and an ad-hoc agent recipe both
import it.

## Why it exists

The browser code used to live in `e2e/lib/`. Only the Playwright suite could
use it. When a person or an agent wanted a manual look, they wrote a second
script with its own selectors. The two copies then disagreed. A hand probe
once declared a working Authentik page blank, because it read `innerText`
across a shadow boundary that the suite already handled.

The harness removes the second copy. One module answers what the login is, what
an app origin is, and what a dashboard tile is. The gate suite and the ad-hoc
recipe agree because they run the same code.

## The library rule

The library never imports `@playwright/test` at run time. It imports the types
only. Three reasons:

1. Playwright refuses to load twice in one process. The e2e runner already
   loads it, so a second copy in the library fails the whole run.
2. An e2e spec and a CLI recipe must be able to use the library with different
   Playwright owners.
3. The library can report a problem without the test runner.

The library observes and acts. It throws an `Error` with a readable message
when something is wrong. The caller asserts. This keeps the same library usable
from a spec, from the CLI, and from a plain Node script.

## The two consumers

### The e2e suite

`e2e/package.json` depends on `@bloud/browser-harness` through a `file:` link.
The spec files import the platform code straight from the package. What stays
in `e2e/` is the runner wiring only:

- `e2e/lib/fixtures.ts` builds the Playwright fixtures.
- `e2e/lib/app-suite.ts` declares the shared behavior ladders.

An assertion that needs the Playwright `expect` surface lives here, next to the
runner.

### An agent recipe

`harness/bin/browser.mjs` is the agent entry. It translates command line
arguments into the environment, then runs `harness/cli/recipe.spec.ts` through
the harness's own Playwright.

```bash
node harness/bin/browser.mjs shot jellyfin          # capture a screenshot
node harness/bin/browser.mjs shot bloud --full      # full dashboard shot
node harness/bin/browser.mjs review immich          # url, title, text, problems
node harness/bin/browser.mjs verify jellyfin        # exit nonzero on problems
node harness/bin/browser.mjs review https://x --anon
```

The three recipes are:

| Recipe | What it does |
|---|---|
| `shot` | Signs in, opens the target, captures a PNG, and prints its path. |
| `review` | Prints the page URL, title, visible text, and the diagnostics report. |
| `verify` | The same report, and exits nonzero when the page recorded a problem. |

A `verify` failure names requests, console errors, page errors, and failed
WebSocket upgrades. It does not fail on a request that is still pending,
because a healthy app can hold a WebSocket open forever.

The target is `bloud` for the dashboard, an app id such as `jellyfin` for that
app's origin, or any `http(s)` URL. `--anon` skips the sign-in.

Screenshots go to `.bloud/screenshots/` by default. The `--out` option names
another directory.

## Install

The CLI runs its own Playwright, so the harness needs its own install:

```bash
npm --prefix harness ci
npm --prefix harness run install-browsers
```

The e2e suite does not need this install. It links the library from its own
`node_modules`, and the library loads no Playwright at run time.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `BLOUD_URL` | `http://localhost:8080` | The public origin (Traefik). |
| `BLOUD_APP_BASE_URL` | `BLOUD_URL` | Overrides the app subdomains only. |
| `BLOUD_API_URL` | `http://localhost:3000` | The host-agent API. |
| `BLOUD_E2E_USERNAME` | `admin` | The sign-in user. |
| `BLOUD_E2E_PASSWORD` | `password` | The sign-in password. |

## Where new code goes

- A new app origin rule, a new dashboard locator, or a new API call goes in
  `harness/src/`.
- A new observation an agent needs goes in `harness/cli/recipe.spec.ts` and
  in the usage text of `harness/bin/browser.mjs`.
- A new test-runner behavior, such as a fixture or a shared ladder, stays in
  `e2e/`.

## Maintenance

`e2e/` links this package through a `file:` dependency, so `e2e/package-lock.json`
records this package's dependencies. A change to `harness/package.json`
dependencies needs a refreshed lock:

```bash
npm --prefix e2e install --package-lock-only
```
