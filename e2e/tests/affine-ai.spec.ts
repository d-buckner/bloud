// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import type { Page } from '@playwright/test';
import { LoginPage } from '../lib/loginPage';
import { appOrigin } from '../lib/origin';

const AFFINE_URL = appOrigin('affine');

// Verifies the whole operator journey end-to-end: SSO login, the workspace, and
// the built-in AI chat answering from the Bloud-wired inference endpoint.
test('AFFiNE AI chat answers from the model', async ({ browser }) => {
  test.setTimeout(600_000);
  const context = await browser.newContext();
  try {
    const affine = await context.newPage();
    await affine.goto(AFFINE_URL);
    await launchOidcFlow(affine);

    // Proven login loop from affine.spec.ts.
    const loginPage = new LoginPage(affine);
    const deadline = Date.now() + 240_000;
    for (;;) {
      if (affine.url().includes('/workspace/')) break;
      if (Date.now() > deadline) break;
      if (await loginPage.isVisible()) {
        await loginPage.login();
        continue;
      }
      await affine.waitForTimeout(500);
    }

    await expect(affine).toHaveURL(/\/workspace\//, { timeout: 180_000 });
    console.log('WORKSPACE:', affine.url());
    // Let the editor settle; Bloud already wired the AI endpoint during the
    // install's PostStart, so no long wait is needed here.
    await affine.waitForTimeout(5000);

    // Open the built-in AI chat panel.
    await affine.getByTestId('ai-chat').click();
    const input = affine.locator('.chat-panel-input textarea').first();
    await input.waitFor({ state: 'visible', timeout: 60_000 });
    await input.focus();
    await input.fill('Reply with exactly one word: pong');
    await affine.keyboard.press('Enter');

    // Wait for the model's actual answer, not just the assistant header
    // ("AFFiNE AI" appears before the streamed content).
    const assistant = affine.locator('[data-testid="chat-message-assistant"]').first();
    await assistant.waitFor({ state: 'visible', timeout: 180_000 });
    const respDeadline = Date.now() + 180_000;
    let response = '';
    while (Date.now() < respDeadline) {
      response = (await assistant.innerText().catch(() => '')).replace(/\s+/g, ' ').trim();
      if (/pong/i.test(response)) break;
      await affine.waitForTimeout(2000);
    }
    console.log('ASSISTANT RESPONSE:', response.slice(0, 600));
    expect(response).toMatch(/pong/i);
  } finally {
    await context.close();
  }
});

async function launchOidcFlow(affine: Page): Promise<void> {
  // With the guest demo workspace disabled, AFFiNE redirects straight to
  // sign-in and there is no editor "Sign in" affordance; with it enabled the
  // editor shows one first. Handle both, and also the case where AFFiNE has
  // already forwarded us to the issuer (Authentik).
  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline) {
    if (affine.url().includes('/if/flow')) return; // already on Authentik
    const oidc = affine.getByRole('button', { name: /continue with oidc/i }).first();
    if ((await oidc.count()) > 0 && (await oidc.isVisible().catch(() => false))) {
      await oidc.click();
      return;
    }
    const signIn = affine.getByRole('button', { name: /sign in/i }).first();
    if ((await signIn.count()) > 0 && (await signIn.isVisible().catch(() => false))) {
      await signIn.click();
      continue; // the OIDC button appears after the modal opens
    }
    await affine.waitForTimeout(500);
  }
}
