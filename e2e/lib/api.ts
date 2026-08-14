// API calls go directly to the host-agent (loopback bypass, no auth needed).
const BASE_URL = process.env.BLOUD_API_URL ?? 'http://localhost:3000';

interface InstalledApp {
  catalog_id: string;
  status: string;
  display_name: string;
  is_system: boolean;
}

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(`${BASE_URL}${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...init?.headers },
  });
  if (!resp.ok) {
    throw new Error(`${init?.method ?? 'GET'} ${path} → ${resp.status}`);
  }
  return resp.json() as Promise<T>;
}

export async function getAppStatus(name: string): Promise<string | null> {
  const body = await fetchJSON<{ apps: InstalledApp[] }>('/api/apps/installed');
  const app = body.apps.find((a) => a.catalog_id === name);
  return app?.status ?? null;
}

export async function installApp(name: string): Promise<void> {
  await fetchJSON('/api/apps/' + name + '/install', {
    method: 'POST',
    body: '{}',
  });
}

export async function waitForApp(
  name: string,
  status: string,
  timeoutMs = 5 * 60_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const current = await getAppStatus(name);
    if (current === status) return;
    await new Promise((r) => setTimeout(r, 3_000));
  }
  throw new Error(
    `Timed out waiting for ${name} to reach "${status}" after ${timeoutMs}ms`,
  );
}

export async function ensureInstalled(name: string): Promise<void> {
  const status = await getAppStatus(name);
  if (status === 'running') return;
  if (!status) {
    await installApp(name);
  }
  await waitForApp(name, 'running');
}
