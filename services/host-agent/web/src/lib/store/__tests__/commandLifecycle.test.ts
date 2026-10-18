// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The command lifecycle, against the real store.
 *
 * Each case is named after the trap it guards. The engine is the subject: dedupe,
 * supersede, refine, the convergence wait, and what a terminal status leaves
 * behind. The store is the singleton because the point is the interaction between
 * a command and the state it waits on, and a mock store would test the mock.
 *
 * Time is fake wherever the wait is the subject, so a ten minute cap costs
 * milliseconds and the backoff schedule can be asserted rather than inferred.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { dispatch, getState } from '../kernel';
import { commandKey } from '../reducers/commands';
import { pendingOverlay } from '../overlay';
import { publicUrlView } from '../selectors';
import { isRunning, runCommand } from '../runner';
import { commandsAbandoned, settingsLoaded } from '../actions';
import { resetStore, toastMessages } from './harness';

const URL_KEY = commandKey('settings', 'publicUrl');

/** The confirmed address, as a read would have stored it. */
function confirmed(url: string): void {
	dispatch(settingsLoaded({ field: 'publicUrl', value: url, at: Date.now() }));
}

beforeEach(() => {
	resetStore();
	vi.useFakeTimers();
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

describe('the request lifecycle', () => {
	it('settles a request-bound command once the response returns', async () => {
		const key = commandKey('rename', 'jellyfin');
		const run = vi.fn().mockResolvedValue('ok');

		const result = await runCommand({ key, run });

		expect(result).toEqual({ ok: true, value: 'ok' });
		expect(isRunning(key)).toBe(false);
		expect(getState().commands.byKey[key].status).toBe('settled');
	});

	it('joins a second call onto the first instead of asking twice', async () => {
		const key = commandKey('install', 'immich');
		let release: (value: number) => void = () => undefined;
		const run = vi.fn().mockReturnValue(new Promise<number>((resolve) => (release = resolve)));

		const first = runCommand({ key, run });
		const second = runCommand({ key, run });

		expect(run).toHaveBeenCalledTimes(1);
		expect(isRunning(key)).toBe(true);

		release(7);
		expect(await first).toEqual({ ok: true, value: 7 });
		expect(await second).toEqual({ ok: true, value: 7 });
	});

	it('records a failure and reports it rather than throwing', async () => {
		const key = commandKey('install', 'jellyfin');

		const result = await runCommand({ key, run: () => Promise.reject(new Error('host agent refused')) });

		expect(result).toEqual({ ok: false, error: 'host agent refused' });
		expect(getState().commands.byKey[key].status).toBe('failed');
		expect(getState().commands.byKey[key].error).toBe('host agent refused');
	});

	it('raises one toast for a rejected write, with the words the operation chose', async () => {
		await runCommand({
			key: commandKey('settings', 'timezone'),
			failure: 'Could not save the time zone',
			run: () => Promise.reject(new Error('400 Bad Request'))
		});

		expect(toastMessages()).toEqual(['Could not save the time zone']);
	});
});

describe('a repeated save', () => {
	it('replaces the first command and drops its claim', async () => {
		let releaseFirst: (value: string) => void = () => undefined;
		const first = runCommand({
			key: URL_KEY,
			repeat: 'replace',
			run: () => new Promise<string>((resolve) => (releaseFirst = resolve)),
			optimistic: { kind: 'settings.publicUrl', key: 'instance', value: { url: 'https://first' } }
		});

		const second = runCommand({
			key: URL_KEY,
			repeat: 'replace',
			run: () => Promise.resolve('second'),
			optimistic: { kind: 'settings.publicUrl', key: 'instance', value: { url: 'https://second' } },
			refine: () => ({ kind: 'settings.publicUrl', key: 'instance', value: { url: 'https://second' } }),
			read: async () => undefined,
			settled: () => true,
			window: { firstMs: 10, maxMs: 10, capMs: 100 }
		});

		// The first command is superseded at once, before the second starts.
		expect(getState().commands.byKey[URL_KEY].status).toBe('pending');
		expect(pendingOverlay(getState().commands, 'settings.publicUrl', 'instance')?.value).toEqual({
			url: 'https://second'
		});

		await vi.advanceTimersByTimeAsync(10);
		expect(await second).toEqual({ ok: true, value: 'second' });

		releaseFirst('first');
		expect(await first).toEqual({ ok: false, error: 'superseded', superseded: true });
	});

	it('raises no toast for the command the user replaced', async () => {
		let releaseFirst: (value: string) => void = () => undefined;
		const first = runCommand({
			key: URL_KEY,
			repeat: 'replace',
			failure: 'Could not save the address',
			run: () => new Promise<string>((resolve) => (releaseFirst = resolve))
		});

		const second = runCommand({
			key: URL_KEY,
			repeat: 'replace',
			run: () => Promise.resolve('second')
		});

		releaseFirst('first');
		await first;
		await second;

		expect(toastMessages()).toEqual([]);
	});
});

describe('the overlay', () => {
	it('is replaced by what the server stored', async () => {
		confirmed('https://old.example');

		const promise = runCommand({
			key: URL_KEY,
			repeat: 'replace',
			run: () => Promise.resolve({ url: 'http://bloud.example.com' }),
			optimistic: { kind: 'settings.publicUrl', key: 'instance', value: { url: 'bloud.example.com' } },
			refine: (res) => ({ kind: 'settings.publicUrl', key: 'instance', value: { url: res.url } }),
			read: async () => undefined,
			settled: (state) => state.settings.publicUrl.value === 'http://bloud.example.com',
			window: { firstMs: 10, maxMs: 10, capMs: 100 }
		});

		// The typed string is shown first, then the canonical origin the parser stored.
		await vi.advanceTimersByTimeAsync(0);
		expect(pendingOverlay(getState().commands, 'settings.publicUrl', 'instance')?.value).toEqual({
			url: 'http://bloud.example.com'
		});

		confirmed('http://bloud.example.com');
		await vi.advanceTimersByTimeAsync(10);

		expect(await promise).toEqual({ ok: true, value: { url: 'http://bloud.example.com' } });
		expect(pendingOverlay(getState().commands, 'settings.publicUrl', 'instance')).toBeNull();
	});

	it('drops at a terminal status, so a failed save shows the confirmed value again', async () => {
		confirmed('https://old.example');
		const key = commandKey('settings', 'timezone');

		await runCommand({
			key,
			optimistic: { kind: 'settings.timezone', key: 'instance', value: { zone: 'Europe/Berlin' } },
			run: () => Promise.reject(new Error('refused'))
		});

		expect(pendingOverlay(getState().commands, 'settings.timezone', 'instance')).toBeNull();
	});

	it('is dropped with the whole record when the module swaps', async () => {
		confirmed('https://old.example');
		const promise = runCommand({
			key: URL_KEY,
			repeat: 'replace',
			run: () => new Promise<string>(() => undefined),
			optimistic: { kind: 'settings.publicUrl', key: 'instance', value: { url: 'https://pending' } }
		});

		expect(publicUrlView(getState()).value).toBe('https://pending');

		dispatch(commandsAbandoned());

		expect(getState().commands.byKey).toEqual({});
		expect(publicUrlView(getState()).value).toBe('https://old.example');
		// The abandoned promise is not awaited: the module that held it is gone.
		void promise;
	});
});

describe('the convergence wait', () => {
	it('checks the confirmed slice, never the value the command shows', async () => {
		confirmed('https://confirmed.example');
		let seen: string | null = null;

		const promise = runCommand({
			key: URL_KEY,
			repeat: 'replace',
			run: () => Promise.resolve({ url: 'https://wanted.example' }),
			optimistic: { kind: 'settings.publicUrl', key: 'instance', value: { url: 'https://wanted.example' } },
			read: async () => undefined,
			settled: (state) => {
				seen = state.settings.publicUrl.value;
				return true;
			},
			window: { firstMs: 10, maxMs: 10, capMs: 100 }
		});

		// The user sees the claim. The predicate must not.
		expect(publicUrlView(getState()).value).toBe('https://wanted.example');
		await vi.advanceTimersByTimeAsync(10);
		await promise;

		expect(seen).toBe('https://confirmed.example');
	});

	it('tolerates a read that fails, because the deadline is the only failure', async () => {
		let reads = 0;
		const promise = runCommand({
			key: URL_KEY,
			repeat: 'replace',
			run: () => Promise.resolve({ url: 'https://wanted.example' }),
			read: async () => {
				reads += 1;
				if (reads < 3) throw new Error('network blip');
				confirmed('https://wanted.example');
			},
			settled: (state) => state.settings.publicUrl.value === 'https://wanted.example',
			window: { firstMs: 100, maxMs: 1_000, capMs: 10_000 }
		});

		await vi.advanceTimersByTimeAsync(100);
		await vi.advanceTimersByTimeAsync(200);
		await vi.advanceTimersByTimeAsync(400);

		expect(await promise).toEqual({ ok: true, value: { url: 'https://wanted.example' } });
		expect(reads).toBe(3);
		expect(toastMessages()).toEqual([]);
	});

	it('backs off from 500ms and doubles to a 5s ceiling', async () => {
		let reads = 0;
		void runCommand({
			key: URL_KEY,
			repeat: 'replace',
			run: () => Promise.resolve({ url: 'https://never.example' }),
			read: async () => {
				reads += 1;
			},
			settled: () => false,
			window: { firstMs: 500, maxMs: 5_000, capMs: 60_000 }
		});

		await vi.advanceTimersByTimeAsync(500);
		expect(reads).toBe(1);
		await vi.advanceTimersByTimeAsync(1_000);
		expect(reads).toBe(2);
		await vi.advanceTimersByTimeAsync(2_000);
		expect(reads).toBe(3);
		await vi.advanceTimersByTimeAsync(4_000);
		expect(reads).toBe(4);
		await vi.advanceTimersByTimeAsync(5_000);
		expect(reads).toBe(5);
		// The ceiling holds instead of doubling again.
		await vi.advanceTimersByTimeAsync(5_000);
		expect(reads).toBe(6);
	});

});

describe('the convergence deadline', () => {
	it('drops the claim and raises one toast when the cap closes unconfirmed', async () => {
		confirmed('https://old.example');
		const promise = runCommand({
			key: URL_KEY,
			repeat: 'replace',
			run: () => Promise.resolve({ url: 'https://never.example' }),
			optimistic: { kind: 'settings.publicUrl', key: 'instance', value: { url: 'https://never.example' } },
			unconfirmed: 'The new address is not live yet',
			read: async () => undefined,
			settled: () => false,
			window: { firstMs: 10, maxMs: 10, capMs: 35 }
		});

		await vi.advanceTimersByTimeAsync(40);
		const result = await promise;

		expect(result.ok).toBe(false);
		if (!result.ok) expect(result.error).toContain('never applied');
		expect(getState().commands.byKey[URL_KEY].status).toBe('failed');
		expect(pendingOverlay(getState().commands, 'settings.publicUrl', 'instance')).toBeNull();
		expect(toastMessages()).toEqual(['The new address is not live yet']);
	});
});
