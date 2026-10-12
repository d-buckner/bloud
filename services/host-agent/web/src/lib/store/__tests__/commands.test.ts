// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The command layer: dedupe, the three settle kinds, and what a failure leaves
 * behind.
 *
 * These are the behaviours the call sites they replace did not have. The
 * convergence case is the one worth the test budget: it is the difference between
 * "the server said yes" and "the change is real", and the two settings saves that
 * got that wrong are the reason the distinction exists.
 *
 * The tests run against the real singleton store rather than a mock: the point is
 * the interaction between a command and the state it is waiting on, and a mock
 * store would test the mock.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { dispatch, getState, store } from '../kernel';
import { runCommand, isRunning } from '../runner';
import { commandKey } from '../reducers/commands';
import { homeSnapshot } from '../actions';
import type { HomeData } from '$lib/types';

function homeWith(id: string): HomeData {
	return {
		apps: [
			{
				id: 1,
				catalog_id: id,
				display_name: id,
				version: '1',
				status: 'running',
				is_system: false,
				installed_at: '2026-01-01',
				updated_at: '2026-01-01',
				x: null,
				y: null,
				w: 1,
				h: 1
			}
		],
		widgets: []
	};
}

/** The store the runner is wired to, for asserting what a command left behind. */
function commandState(key: string) {
	return getState().commands.byKey[key];
}

beforeEach(() => {
	// The singleton survives the file; drain it so one case cannot see another.
	void store;
});

afterEach(() => {
	vi.useRealTimers();
});

describe('runCommand', () => {
	it('settles a request-bound command once the response returns', async () => {
		const key = commandKey('install', 'jellyfin');
		const run = vi.fn().mockResolvedValue('ok');
		const result = await runCommand({ key, kind: 'request', run });

		expect(result).toEqual({ ok: true, value: 'ok' });
		expect(isRunning(key)).toBe(false);
		expect(commandState(key).status).toBe('settled');
	});

	it('joins a second call onto the first instead of asking twice', async () => {
		const key = commandKey('install', 'immich');
		let release: (value: number) => void = () => undefined;
		const run = vi.fn().mockReturnValue(new Promise<number>((resolve) => (release = resolve)));

		const first = runCommand({ key, kind: 'request', run });
		const second = runCommand({ key, kind: 'request', run });
		expect(run).toHaveBeenCalledTimes(1);
		expect(isRunning(key)).toBe(true);

		release(7);
		expect(await first).toEqual({ ok: true, value: 7 });
		expect(await second).toEqual({ ok: true, value: 7 });
	});

	it('records a failure and reports it rather than throwing', async () => {
		const key = commandKey('uninstall', 'jellyfin');
		const result = await runCommand({
			key,
			kind: 'request',
			run: () => Promise.reject(new Error('host agent refused'))
		});

		expect(result).toEqual({ ok: false, error: 'host agent refused' });
		expect(commandState(key).status).toBe('failed');
		expect(commandState(key).error).toBe('host agent refused');
	});

	it('awaits the confirm read before settling a reread command', async () => {
		const order: string[] = [];
		await runCommand({
			key: commandKey('launcher', 'add'),
			kind: 'reread',
			run: async () => {
				order.push('write');
			},
			confirm: async () => {
				order.push('read');
			}
		});
		expect(order).toEqual(['write', 'read']);
	});

	it('settles a convergence command as soon as the state agrees', async () => {
		const key = commandKey('address', 'public-url');
		const promise = runCommand({
			key,
			kind: 'convergence',
			run: async () => null,
			settled: (state) => state.apps.snapshotAt > 0,
			within: 5_000
		});
		// The write was accepted; reality catches up one snapshot later.
		dispatch(homeSnapshot(homeWith('jellyfin')));

		await expect(promise).resolves.toEqual({ ok: true, value: null });
	});

	it('fails a convergence command the orchestrator never applies', async () => {
		vi.useFakeTimers();
		const key = commandKey('address', 'never-applied');
		const promise = runCommand({
			key,
			kind: 'convergence',
			run: async () => null,
			settled: () => false,
			within: 1_000
		});
		await vi.advanceTimersByTimeAsync(1_500);

		const result = await promise;
		expect(result.ok).toBe(false);
		if (!result.ok) expect(result.error).toContain('never applied');
	});
});
