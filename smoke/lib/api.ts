import type { APIRequestContext } from '@playwright/test';

interface SetupStatus {
  setupRequired: boolean;
  authentikReady: boolean;
  authReady: boolean;
}

interface CreateUserResponse {
  success: boolean;
  error?: string;
}

export class SmokeApi {
  // When BLOUD_VM_IP is set, use the VM IP directly for Node.js HTTP requests to
  // bypass mDNS resolution failures across network boundaries (e.g. WiFi ↔ wired).
  // The Host header ensures Authentik's forward-auth outpost matches the domain.
  private readonly apiBase = process.env.BLOUD_VM_IP
    ? `http://${process.env.BLOUD_VM_IP}`
    : (process.env.BLOUD_URL ?? 'http://bloud.local');
  private readonly hostHeader = process.env.BLOUD_VM_IP ? { Host: 'bloud.local' } : {};

  constructor(private readonly request: APIRequestContext) {}

  async waitForSetupReady(timeoutMs = 15 * 60 * 1000): Promise<void> {
    const start = Date.now();
    let inlineActive = false;

    while (Date.now() - start < timeoutMs) {
      const elapsed = Math.floor((Date.now() - start) / 1000);

      let response;
      try {
        response = await this.request.get(`${this.apiBase}/api/setup/status`, {
          headers: this.hostHeader,
        });
      } catch {
        process.stdout.write(`\r\x1b[K  Waiting for install to complete... [${elapsed}s]`);
        inlineActive = true;
        await sleep(5_000);
        continue;
      }

      if (!response.ok()) {
        process.stdout.write(`\r\x1b[K  Waiting for install to complete... [${elapsed}s]`);
        inlineActive = true;
        await sleep(5_000);
        continue;
      }

      if (inlineActive) {
        process.stdout.write('\n');
        inlineActive = false;
      }

      const data: SetupStatus = await response.json();
      const forwardAuth = await this.isForwardAuthReady();
      process.stdout.write(
        `  [${elapsed}s] authentik=${r(data.authentikReady)}, auth=${r(data.authReady)}, forwardAuth=${r(forwardAuth)}\n`,
      );
      if (data.authentikReady && data.authReady && forwardAuth) return;

      await sleep(5_000);
    }

    if (inlineActive) process.stdout.write('\n');
    throw new Error('Timeout waiting for setup to be ready (15 minutes)');
  }

  // Verifies the Authentik proxy outpost is healthy and connected by pinging its
  // health endpoint. The API may report authentikReady before the outpost has
  // loaded its provider config; this ensures the outpost is truly operational.
  private async isForwardAuthReady(): Promise<boolean> {
    try {
      const response = await this.request.get(
        `${this.apiBase}/outpost.goauthentik.io/ping`,
        { headers: this.hostHeader },
      );
      return response.status() === 204;
    } catch {
      return false;
    }
  }

  async createUser(username: string, password: string): Promise<void> {
    const response = await this.request.post(`${this.apiBase}/api/setup/create-user`, {
      data: { username, password },
      headers: this.hostHeader,
    });
    if (!response.ok()) {
      const text = await response.text();
      throw new Error(`Failed to create user: ${response.status()} - ${text}`);
    }
    const data: CreateUserResponse = await response.json();
    if (!data.success) {
      throw new Error(`Failed to create user: ${data.error}`);
    }
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function r(v: boolean): string {
  return v ? 'ready' : 'waiting';
}
