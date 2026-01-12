import { execSync } from 'child_process';
import { writeFileSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

const PROJECT_ROOT = join(__dirname, '../..');
const LIMA_TEST = join(PROJECT_ROOT, 'lima/test');
const STATE_FILE = join(__dirname, '../.test-state.json');

// Test ports (different from dev)
const API_URL = process.env.API_URL || 'http://localhost:3001';
const TRAEFIK_URL = process.env.BASE_URL || 'http://localhost:8081';
const VITE_URL = process.env.VITE_URL || 'http://localhost:5174';

interface TestState {
  vmCreated: boolean;
  servicesReady: boolean;
  startTime: number;
}

function log(msg: string) {
  console.log(`[global-setup] ${msg}`);
}

function exec(cmd: string, options: { timeout?: number } = {}) {
  const timeout = options.timeout || 120000;
  try {
    return execSync(cmd, {
      encoding: 'utf8',
      timeout,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
  } catch (error) {
    const e = error as { stderr?: string; message?: string };
    throw new Error(`Command failed: ${cmd}\n${e.stderr || e.message}`);
  }
}

function runLimaTest(command: string, timeout = 120000): string {
  return exec(`${LIMA_TEST} ${command}`, { timeout });
}

async function waitForService(
  name: string,
  url: string,
  timeoutMs = 60000
): Promise<boolean> {
  const start = Date.now();
  log(`Waiting for ${name} at ${url}...`);

  while (Date.now() - start < timeoutMs) {
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(5000) });
      if (response.ok) {
        log(`${name} is ready`);
        return true;
      }
    } catch {
      // Service not ready yet
    }
    await new Promise((r) => setTimeout(r, 2000));
  }

  log(`${name} failed to become ready within ${timeoutMs / 1000}s`);
  return false;
}

async function waitForAllServices(): Promise<boolean> {
  // Wait for Go API on test port
  const apiReady = await waitForService('Go API', `${API_URL}/api/health`, 60000);
  if (!apiReady) return false;

  // Wait for Vite on test port
  const viteReady = await waitForService('Vite', VITE_URL, 30000);
  if (!viteReady) return false;

  // Wait for Traefik on test port
  const traefikReady = await waitForService('Traefik', TRAEFIK_URL, 30000);
  if (!traefikReady) return false;

  return true;
}

function saveState(state: TestState) {
  writeFileSync(STATE_FILE, JSON.stringify(state, null, 2));
}

export default async function globalSetup() {
  const skipVmLifecycle = process.env.SKIP_VM_LIFECYCLE === 'true';

  log('Starting global setup...');
  log(`Test ports: API=${API_URL}, Vite=${VITE_URL}, Traefik=${TRAEFIK_URL}`);

  const state: TestState = {
    vmCreated: false,
    servicesReady: false,
    startTime: Date.now(),
  };

  try {
    if (skipVmLifecycle) {
      // VM lifecycle handled by run-integration-tests script
      log('VM lifecycle handled externally (SKIP_VM_LIFECYCLE=true)');
      log('Waiting for services to be ready...');
      const ready = await waitForAllServices();
      if (!ready) {
        throw new Error('Test services not ready - ensure VM is running');
      }
      state.servicesReady = true;
      log('Services ready');
    } else {
      // Create fresh test VM for maximum isolation
      log('Creating fresh test VM (this may take a few minutes)...');
      runLimaTest('vm-create', 600000); // 10 minute timeout for VM creation + NixOS rebuild
      state.vmCreated = true;

      // Start test services
      log('Starting test services...');
      runLimaTest('start', 60000);

      // Wait for services on test ports
      log('Waiting for services to be ready...');
      const ready = await waitForAllServices();

      if (!ready) {
        throw new Error('Test services failed to start within timeout');
      }

      state.servicesReady = true;
      log('Global setup complete - fresh test VM ready with all services');
    }
  } finally {
    saveState(state);
  }
}
