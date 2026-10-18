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

`harness/bin/browser.mjs` is the agent entry. It reads the command line, sets the
recipe in the environment, then runs `harness/cli/recipe.spec.ts` through the
harness's own Playwright.

```bash
node harness/bin/browser.mjs shot jellyfin          # capture a screenshot
node harness/bin/browser.mjs shot bloud --full      # full dashboard shot
node harness/bin/browser.mjs review immich          # url, title, text, problems
node harness/bin/browser.mjs verify jellyfin        # exit nonzero on problems
node harness/bin/browser.mjs review https://x --anon
node harness/bin/browser.mjs apps                   # what is installed, and its state
node harness/bin/browser.mjs status                 # health, setup, address, clock
node harness/bin/browser.mjs api GET /api/system/diagnostics
node harness/bin/browser.mjs run app-health jellyfin  # a targeted check
node harness/bin/browser.mjs checks                 # list the built-in checks
```

The recipes are:

| Recipe | What it does |
|---|---|
| `shot` | Signs in, opens the target, captures a PNG, and prints its path. |
| `review` | Prints the page URL, title, visible text, and the diagnostics report. |
| `verify` | The same report, and exits nonzero when the page recorded a problem. |
| `apps` | Lists installed apps with their state, port, and the orchestrator's own words. |
| `status` | Prints health, setup, the public URL, the clock, and DNS diagnostics. |
| `api` | Sends one call to the host-agent API and prints the response. |
| `run <check>` | Runs a check: a targeted validation, written as one small file. |
| `checks` | Lists the built-in checks in `harness/checks/`. |

A `verify` failure names requests, console errors, page errors, and failed
WebSocket upgrades. It does not fail on a request that is still pending,
because a healthy app can hold a WebSocket open forever.

The target is `bloud` for the dashboard, an app id such as `jellyfin` for that
app's origin, or any `http(s)` URL. `--anon` skips the sign-in.

Screenshots go to `.bloud/screenshots/` by default. The `--out` option names
another directory.

The `api` recipe reads the operator token the same way the CLI does, so a call
that needs admin works with no extra setup. `BLOUD_API_TOKEN` overrides it.

## Write a targeted check

A check answers one question about a running instance, and it is one small file.
The runner signs in, opens the target, calls your function, prints one line per
assertion, and exits nonzero when any assertion failed.

This is the whole contract:

```js
// check-immich.mjs
export default async function ({ api, check, open }) {
  const immich = await api.getAppStatus('immich');
  check.ok(immich === 'running', 'the orchestrator reports Immich as running', `its state is "${immich}"`);

  await open('immich');
  await check.textIncludes(/login|assets/i, 'the Immich page renders its own content');
  await check.noProblemsIgnoringSignIn('Immich loaded without problems');
}
```

```bash
node harness/bin/browser.mjs run ./check-immich.mjs
```

A check file is plain JavaScript, and it needs no import for the common case:
the runner hands over the library, the API client, the page, and the collector.
That is what lets `browser.mjs checks` list a built-in without a browser and
without a TypeScript transform.

### What the context holds

| Field | What it is |
|---|---|
| `check` | The assertions. Each one records a result and returns its boolean. |
| `page` | The Playwright page, signed in unless the run passed `--anon`. |
| `open(target?)` | Opens a target, or the one the command named. |
| `shot(name?)` | Captures a screenshot and returns its path. |
| `note(message)` | Prints a line of evidence while the run is still going. |
| `target` | What the command named: `bloud`, an app id, or a URL. |
| `args` | The positional arguments after the target. |
| `origin(app)` | The origin an app's subdomain is served under. |
| `api` | The host-agent API client. |
| `harness` | The whole library, for anything the context does not lift. |
| `session` | The session that owns the page: `open`, `review`, `screenshot`, `diag`. |
| `diag` | What the page recorded: requests, console, page errors, sockets. |
| `newPage({ signedIn })` | A second page in its own browser context, signed in or not. |

Ask for a second page instead of navigating the first one away. The run's own
page keeps its sign-in, so an anonymous leg and a signed-in leg can both run in
one check.

### The assertions

| Assertion | What it asks |
|---|---|
| `ok(condition, label, detail?)` | The general case. `detail` explains a failure. |
| `fail(label, detail?)` | Records a failure you already decided on. |
| `skip(label, reason)` | Records a question this instance cannot answer. A skip is not a pass. |
| `eq(actual, expected, label)` | Compares values, including objects, and prints both sides. |
| `match(value, pattern, label)` | Matches a string against a pattern. |
| `includes(haystack, needle, label)` | Asks whether a string contains a substring. |
| `textIncludes(pattern, label)` | Reads the page text through open shadow roots, then matches. |
| `titleIs(pattern, label)` | Matches the page title. |
| `urlIs(pattern, label)` | Matches the page URL. |
| `visible(target, label)` | Asks whether a locator is on screen. |
| `appears(selector, label, ms?)` | Waits for a selector to exist. |
| `poll(label, condition, ms?)` | Waits for a condition, and reports the wait it cost. |
| `noProblems(label?, ignore?)` | Asserts the page recorded no failed request, console error, page error, or dead socket. |
| `noProblemsIgnoringSignIn(label?)` | The same, and prints the sign-in round-trip problems it set aside. |
| `must(label, step)` | Runs a step the rest of the check depends on. A failure stops the check. |

A pattern is a plain substring, or `/source/flags` for a regular expression. A
path such as `/if/flow` stays the path it looks like: only the flags JavaScript
knows make a pattern.

Use `check.must` for anything the next line cannot survive. A step that throws is
recorded as a failure with its own message, and the check stops there. Without
it, one dead address produces ten failures and hides which one to fix.

### What a check reports

The report prints one line per assertion, then a summary:

```
app health
  PASS jellyfin is running  (22ms)
  PASS the app address answers  (41ms)
  FAIL the signed-in page renders content  (1.9s)
        the page is blank at https://jellyfin.localhost:8080/
3 checks: 2 passed, 1 failed in 2.0s
```

`--json` prints the same facts as data: `ok`, the counts, and one result per
assertion with its label, detail, and duration. `note` lines stream as the run
goes, so a check that takes two minutes says what it is doing.

### Built-in checks

`harness/checks/` holds the checks that ship with the harness. Each one is a
worked example of the shape above.

| Check | What it answers |
|---|---|
| `api-smoke` | Does the API answer, and does it agree with itself? |
| `app-health <app>` | One app: orchestrator state, its own address, a signed-in page, an anonymous one. |
| `dashboard` | Does the dashboard render the grid, the catalog, and the gate? |
| `settings-roundtrip` | Does a setting saved through the API reach the page and come back? |

```bash
node harness/bin/browser.mjs checks                 # list them
node harness/bin/browser.mjs run app-health jellyfin
node harness/bin/browser.mjs run ./my-check.mjs some-target --arg
```

A built-in may name its own default target in `export const options`. The
command line wins when the caller names one.

### Rules a check follows

- Name each assertion so the line reads as an answer. "the app converged" beats
  "status is running".
- Print what you saw in the `detail` of a failure. The reader is not in the
  browser.
- Set aside the sign-in round trip, and say you did. An app that delegates
  authentication answers 401 on its own session probe until the identity
  provider has finished. `noProblemsIgnoringSignIn` exists so a check does not
  call that a fault, and does not hide it either.
- Wait for the page, do not sleep. `open` settles the page; `appears` and `poll`
  wait for a condition.
- One check, one question. Two unrelated questions in one file produce a report
  that names two things when one broke.

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
- A check worth keeping goes in `harness/checks/`, so the next person runs it
  instead of writing it again. A check for one question, once, can live outside
  the repository and pass its path to `run`.
- A new test-runner behavior, such as a fixture or a shared ladder, stays in
  `e2e/`.

## Tests

`harness/test/` holds the unit tests for the parts an agent writes against: the
check collector, the report text, and the command line. They drive no browser.

```bash
npm --prefix harness test        # vitest
npm --prefix harness run check   # tsc --noEmit
```

The fast validation tier runs both when a file under `harness/` changes.

## Maintenance

`e2e/` links this package through a `file:` dependency, so `e2e/package-lock.json`
records this package's dependencies. A change to `harness/package.json`
dependencies needs a refreshed lock:

```bash
npm --prefix e2e install --package-lock-only
```
