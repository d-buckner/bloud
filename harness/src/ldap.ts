// SPDX-License-Identifier: AGPL-3.0-only
//
// The readiness barrier for an app that verifies a person against the identity
// provider's LDAP outpost.
//
// Two things must hold before such an app can answer a credential, and the
// dependency graph only proves the first one. The app node has to reach
// RUNNING, and the app has to be able to reach the outpost. Re-provisioning
// Authentik restarts the outpost, and every install re-provisions it. The app
// then resolves the outpost's container name again on each login, and that
// lookup can fail for a window after a convergence pass. Both warm slowly: the
// CI runs measured in docs/plans/ci-flakiness-reduction.md lost about 20
// seconds to a single failed lookup.
//
// The Go integration suite has carried this barrier since the F2 fix:
// `waitLDAPReady` plus `davRequestSettled` in
// services/host-agent/internal/e2e/. The browser suite had neither, so the
// same race kept failing there. This module is that barrier for the browser
// tier, and both tiers read the same definition of "not ready yet".
import type { APIResponse } from '@playwright/test';

/**
 * Statuses where the app answered but its LDAP backend could not finish.
 *
 * Radicale turns an outpost that will not complete a bind into a 500, and a
 * proxy in front of an app that is not serving turns the same outage into a
 * 502 or a 503. None of the three says "the provider is still warming" on the
 * wire, so a correct credential inside that window is indistinguishable from a
 * broken app.
 *
 * These are the same three codes the app configurator's own PostStart
 * classifies as the provider's outage, and the same three `ldapBackendWarming`
 * accepts in services/host-agent/internal/e2e/radicale_test.go. The two
 * harnesses must agree about what to retry, or one of them retries a real
 * failure and hides it.
 */
const WARMING_STATUSES = new Set([500, 502, 503]);

/**
 * Per-attempt request budget for a call through an LDAP-backed app.
 *
 * It has to outlast one resolver stall. An attempt cut off at the global
 * `actionTimeout` of 10 seconds reports the client's own timeout instead of
 * whatever the app answers at 20, and that is the difference between a failure
 * a reader can diagnose and one that points at the wrong component.
 */
export const LDAP_ATTEMPT_TIMEOUT_MS = 45_000;

/**
 * Total budget for waiting out the LDAP warm window.
 *
 * The window is not the app's boot; the convergence rungs already cover that.
 * It is the outpost restart that every re-provision causes, plus the container
 * name lookup that fails behind it. Measured on CI, one failed lookup costs the
 * container resolver about 20 seconds before it gives up, and more than one can
 * land in a run. Three minutes covers several of those stalls and still fails
 * well inside the Playwright per-test cap.
 */
export const LDAP_SETTLE_TIMEOUT_MS = 180_000;

/** True when `status` means the app answered but its LDAP backend is not ready. */
export function isLdapBackendWarming(status: number): boolean {
  return WARMING_STATUSES.has(status);
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/**
 * Sends one request against the app, retried until the answer settles.
 *
 * `send` is a factory, not a response: each attempt has to re-send the request,
 * because a response body can only be read once.
 *
 * The loop returns the first answer that is not a warming status. Once the
 * deadline passes it returns the last warming answer rather than throwing, so
 * the caller's own assertion names the status the app really gave. A retry that
 * quietly swallowed a settled-but-wrong status would hide the exact thing the
 * caller exists to detect.
 *
 * A send that throws is retried too. The failure this exists for is a request
 * that never completes: the container's resolver exhausts its own budget
 * before the app answers, so the client sees a timeout and no status at all.
 * If every attempt throws, the deadline produces an error that carries the last
 * failure instead of a bare timeout.
 */
export async function waitForLdapBackedAnswer(
  label: string,
  send: () => Promise<APIResponse>,
  timeoutMs: number,
  intervalMs = 2_000,
): Promise<APIResponse> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    let res: APIResponse | undefined;
    let failure = '';
    try {
      res = await send();
    } catch (err) {
      failure = err instanceof Error ? err.message : String(err);
    }
    if (res && !isLdapBackendWarming(res.status())) return res;
    if (Date.now() >= deadline) {
      if (res) return res;
      throw new Error(
        `${label}: the app never answered within ${timeoutMs}ms ` +
          `(last failure: ${failure || 'no response'})`,
      );
    }
    // Only the discarded answers need releasing: the one returned below still
    // has a body the caller has to read.
    if (res) await res.dispose().catch(() => {});
    await delay(intervalMs);
  }
}
