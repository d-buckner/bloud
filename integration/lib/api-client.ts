import type { APIRequestContext } from '@playwright/test';

export interface App {
  name: string;
  display_name: string;
  description: string;
  category: string;
  status: 'running' | 'starting' | 'installing' | 'uninstalling' | 'stopped' | 'error';
  port: number;
  is_system: boolean;
  version?: string;
  installed_at?: string;
}

export interface CatalogApp {
  name: string;
  displayName: string;
  description: string;
  category: string;
  port: number;
  isSystem: boolean;
  integrations?: {
    requires?: Array<{ name: string; recommendedApp?: string }>;
    optional?: Array<{ name: string; recommendedApp?: string }>;
  };
  healthCheck?: {
    path: string;
    interval: string;
    timeout: string;
  };
}

export interface InstallChoice {
  integration: string;
  choice: string;
}

/**
 * API client for interacting with the Bloud host-agent API.
 */
export class ApiClient {
  private baseUrl: string;
  private request: APIRequestContext;

  constructor(request: APIRequestContext, baseUrl?: string) {
    this.request = request;
    // Default to test port (3001) - different from dev port (3000)
    this.baseUrl = baseUrl || process.env.API_URL || 'http://localhost:3001';
  }

  /**
   * Get all apps from the catalog.
   */
  async getCatalog(): Promise<CatalogApp[]> {
    const response = await this.request.get(`${this.baseUrl}/api/apps`);
    if (!response.ok()) {
      throw new Error(`Failed to get catalog: ${response.status()}`);
    }
    return response.json();
  }

  /**
   * Get all installed apps.
   */
  async getInstalledApps(): Promise<App[]> {
    const response = await this.request.get(`${this.baseUrl}/api/apps/installed`);
    if (!response.ok()) {
      throw new Error(`Failed to get installed apps: ${response.status()}`);
    }
    const data = await response.json();
    // API returns array directly
    return Array.isArray(data) ? data : data.apps || [];
  }

  /**
   * Get a specific installed app by name.
   */
  async getApp(name: string): Promise<App | null> {
    const apps = await this.getInstalledApps();
    return apps.find((a) => a.name === name) || null;
  }

  /**
   * Install an app with optional integration choices.
   */
  async installApp(name: string, choices?: InstallChoice[]): Promise<void> {
    const body = choices && choices.length > 0 ? { choices } : {};
    const response = await this.request.post(`${this.baseUrl}/api/apps/${name}/install`, {
      data: body,
    });
    if (!response.ok()) {
      const text = await response.text();
      throw new Error(`Failed to install ${name}: ${response.status()} - ${text}`);
    }
  }

  /**
   * Uninstall an app.
   */
  async uninstallApp(name: string): Promise<void> {
    const response = await this.request.post(`${this.baseUrl}/api/apps/${name}/uninstall`);
    if (!response.ok()) {
      const body = await response.text();
      throw new Error(`Failed to uninstall ${name}: ${response.status()} - ${body}`);
    }
  }

  /**
   * Wait for an app to reach a specific status.
   */
  async waitForAppStatus(
    name: string,
    targetStatus: App['status'] | App['status'][],
    timeoutMs = 30_000
  ): Promise<App> {
    const statuses = Array.isArray(targetStatus) ? targetStatus : [targetStatus];
    const start = Date.now();

    while (Date.now() - start < timeoutMs) {
      const app = await this.getApp(name);
      if (app && statuses.includes(app.status)) {
        return app;
      }
      await new Promise((resolve) => setTimeout(resolve, 1000));
    }

    const app = await this.getApp(name);
    throw new Error(
      `Timeout waiting for ${name} to reach ${statuses.join('|')}. ` +
        `Current status: ${app?.status || 'not found'}`
    );
  }

  /**
   * Wait for an app to be uninstalled (no longer in installed list).
   */
  async waitForAppUninstalled(name: string, timeoutMs = 30_000): Promise<void> {
    const start = Date.now();

    while (Date.now() - start < timeoutMs) {
      const app = await this.getApp(name);
      if (!app) {
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, 1000));
    }

    throw new Error(`Timeout waiting for ${name} to be uninstalled`);
  }

  /**
   * Check if the API is healthy.
   */
  async isHealthy(): Promise<boolean> {
    try {
      const response = await this.request.get(`${this.baseUrl}/api/health`);
      return response.ok();
    } catch {
      return false;
    }
  }

  /**
   * Get system status.
   */
  async getSystemStatus(): Promise<Record<string, unknown>> {
    const response = await this.request.get(`${this.baseUrl}/api/system/status`);
    if (!response.ok()) {
      throw new Error(`Failed to get system status: ${response.status()}`);
    }
    return response.json();
  }

  /**
   * Get user-facing apps (non-system apps).
   */
  async getUserApps(): Promise<App[]> {
    const apps = await this.getInstalledApps();
    return apps.filter((a) => !a.is_system);
  }

  /**
   * Ensure an app is installed and running. Installs if needed.
   */
  async ensureAppRunning(name: string, choices?: InstallChoice[]): Promise<App> {
    let app = await this.getApp(name);

    if (!app) {
      await this.installApp(name, choices);
      app = await this.waitForAppStatus(name, 'running');
    } else if (app.status !== 'running') {
      app = await this.waitForAppStatus(name, 'running');
    }

    return app;
  }

  /**
   * Ensure an app is uninstalled.
   */
  async ensureAppUninstalled(name: string): Promise<void> {
    const app = await this.getApp(name);
    if (app) {
      await this.uninstallApp(name);
      await this.waitForAppUninstalled(name);
    }
  }

  /**
   * Clear all data for an app (uninstalls, deletes data directory and database).
   */
  async clearAppData(name: string): Promise<void> {
    const response = await this.request.post(`${this.baseUrl}/api/apps/${name}/clear-data`);
    if (!response.ok()) {
      const text = await response.text();
      throw new Error(`Failed to clear data for ${name}: ${response.status()} - ${text}`);
    }
  }
}
