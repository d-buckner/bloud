import type { APIRequestContext } from '@playwright/test';

interface SetupStatus {
  setupRequired: boolean;
  authentikReady: boolean;
}

interface CreateUserResponse {
  success: boolean;
  error?: string;
}

export class SmokeApi {
  private readonly baseUrl = 'http://bloud.local';

  constructor(private readonly request: APIRequestContext) {}

  async waitForSetupReady(timeoutMs = 10 * 60 * 1000): Promise<void> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
      try {
        const response = await this.request.get(`${this.baseUrl}/api/setup/status`);
        if (response.ok()) {
          const data: SetupStatus = await response.json();
          if (data.authentikReady) return;
        }
      } catch {
        // not up yet
      }
      await sleep(5_000);
    }
    throw new Error('Timeout waiting for Authentik to be ready (10 minutes)');
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
