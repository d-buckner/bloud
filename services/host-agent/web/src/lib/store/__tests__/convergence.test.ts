// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The convergence window: how long a settings save is allowed to take to land,
 * and what the poll hands back when it has not.
 *
 * The poll exists because a 202 is a receipt and not a result. What these tests
 * pin down is the part a caller cannot see from outside: that the first read is
 * immediate, that the loop stops the moment the value settles, that running out
 * of time returns the last value instead of throwing (so the caller gets to say
 * what the failure means in its own words), and that a read that fails is not
 * swallowed into a retry loop.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { pollUntil } from '../effects/convergence';

const WINDOW = { intervalMs: 500, timeoutMs: 5_000 };

describe('pollUntil', () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});

	afterEach(() => {
		vi.useRealTimers();
	});

	it('returns the first read when it already settled', async () => {
		const read = vi.fn(async () => 7);

		await expect(pollUntil(read, (v) => v === 7, WINDOW)).resolves.toBe(7);
		expect(read).toHaveBeenCalledTimes(1);
	});

	it('reads again until the value settles, then stops reading', async () => {
		let value = 0;
		const read = vi.fn(async () => ++value);

		const done = pollUntil(read, (v) => v === 3, WINDOW);
		await vi.advanceTimersByTimeAsync(1_000);

		await expect(done).resolves.toBe(3);
		expect(read).toHaveBeenCalledTimes(3);
	});

	it('returns the last value it saw when the window closes', async () => {
		const read = vi.fn(async () => 'stale');

		const done = pollUntil(read, (v) => v === 'fresh', { intervalMs: 500, timeoutMs: 2_000 });
		await vi.advanceTimersByTimeAsync(3_000);

		await expect(done).resolves.toBe('stale');
		// The immediate read plus the ones that still fit inside the window.
		expect(read).toHaveBeenCalledTimes(5);
	});

	it('lets a failed read reach the caller', async () => {
		const done = pollUntil(
			async () => {
				throw new Error('api down');
			},
			() => true,
			WINDOW
		);

		await expect(done).rejects.toThrow('api down');
	});
});
