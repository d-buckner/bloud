import type { APIRequestContext } from '@playwright/test';

interface SetupStatus {
  setupRequired: boolean;
  authentikReady: boolean;
}

interface CreateUserResponse {
  success: boolean;
  error?: string;
}

interface InstalledApp {
  name: string;
  status: 'running' | 'starting' | 'installing' | 'uninstalling' | 'stopped' | 'error';
}

export class SmokeApi {
  private readonly baseUrl = 'http://bloud.local';

  constructor(private readonly request: APIRequestContext) {}

  async waitForSetupReady(timeoutMs = 5 * 60 * 1000): Promise<void> {
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
    throw new Error('Timeout waiting for Authentik to be ready (5 minutes)');
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

  async installApp(name: string): Promise<void> {
    const response = await this.request.post(`${this.baseUrl}/api/apps/${name}/install`, {
      data: {},
    });
    if (!response.ok()) {
      const text = await response.text();
      throw new Error(`Failed to install ${name}: ${response.status()} - ${text}`);
    }
  }

  async waitForAppRunning(name: string, timeoutMs = 5 * 60 * 1000): Promise<void> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
      try {
        const response = await this.request.get(`${this.baseUrl}/api/apps/installed`);
        if (response.ok()) {
          const apps: InstalledApp[] = await response.json();
          const app = apps.find((a) => a.name === name);
          if (app?.status === 'running') return;
        }
      } catch {
        // not up yet
      }
      await sleep(10_000);
    }
    throw new Error(`Timeout waiting for ${name} to reach running state (5 minutes)`);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
