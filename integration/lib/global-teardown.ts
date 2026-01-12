import { execSync } from 'child_process';
import { existsSync, readFileSync, rmSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

const PROJECT_ROOT = join(__dirname, '../..');
const LIMA_TEST = join(PROJECT_ROOT, 'lima/test');
const STATE_FILE = join(__dirname, '../.test-state.json');

interface TestState {
  vmCreated: boolean;
  servicesReady: boolean;
  startTime: number;
}

function log(msg: string) {
  console.log(`[global-teardown] ${msg}`);
}

function exec(cmd: string) {
  try {
    return execSync(cmd, {
      encoding: 'utf8',
      timeout: 120000,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
  } catch {
    // Ignore errors during teardown
  }
}

function runLimaTest(command: string) {
  exec(`${LIMA_TEST} ${command}`);
}

function loadState(): TestState | null {
  if (!existsSync(STATE_FILE)) {
    return null;
  }
  try {
    return JSON.parse(readFileSync(STATE_FILE, 'utf8'));
  } catch {
    return null;
  }
}

export default async function globalTeardown() {
  const keepTestVM = process.env.KEEP_TEST_VM === 'true';
  const skipVmLifecycle = process.env.SKIP_VM_LIFECYCLE === 'true';

  log('Starting global teardown...');

  const state = loadState();
  if (state) {
    const duration = ((Date.now() - state.startTime) / 1000).toFixed(1);
    log(`Test session duration: ${duration}s`);
  }

  // Clean up state file
  if (existsSync(STATE_FILE)) {
    rmSync(STATE_FILE);
  }

  if (skipVmLifecycle) {
    log('VM lifecycle handled externally (SKIP_VM_LIFECYCLE=true)');
    return;
  }

  if (keepTestVM) {
    log('Keeping test VM running (KEEP_TEST_VM=true)');
    log('Test services are still running - access at:');
    log('  Web UI: http://localhost:8081');
    log('  API: http://localhost:3001');
    log('');
    log('To destroy later: ./lima/test vm-destroy');
    return;
  }

  // Destroy the test VM completely
  log('Destroying test VM...');
  runLimaTest('vm-destroy');

  log('Teardown complete - test VM destroyed');
}
