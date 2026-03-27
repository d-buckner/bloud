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
  private readonly baseUrl = process.env.BLOUD_URL ?? 'http://bloud.local';

  constructor(private readonly request: APIRequestContext) {}

  async waitForSetupReady(timeoutMs = 15 * 60 * 1000): Promise<void> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
      try {
        const response = await this.request.get(`${this.baseUrl}/api/setup/status`);
        if (response.ok()) {
          const data: SetupStatus = await response.json();
          if (data.authentikReady && data.authReady && (await this.isForwardAuthReady())) return;
        }
      } catch {
        // not up yet
      }
      await sleep(5_000);
    }
    throw new Error('Timeout waiting for Authentik to be ready (15 minutes)');
  }

  // Verifies the forward-auth redirect chain is working by checking that an
  // unauthenticated request to / is redirected to the Authentik login flow.
  // The API may report "ready" before the outpost has loaded its config.
  private async isForwardAuthReady(): Promise<boolean> {
    try {
      const response = await this.request.get(`${this.baseUrl}/`);
      return response.url().includes('/if/flow/');
    } catch {
      return false;
    }
  }

  async createUser(username: string, password: string): Promise<void> {
    const response = await this.request.post(`${this.baseUrl}/api/setup/create-user`, {
      data: { username, password },
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
