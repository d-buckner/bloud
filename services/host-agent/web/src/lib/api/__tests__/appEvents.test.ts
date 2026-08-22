// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { describe, it, expect } from 'vitest';
import { shouldEnableFallback, FALLBACK_DELAY_MS } from '../appEvents';

const T0 = 1_700_000_000_000;

describe('shouldEnableFallback', () => {
	it('stays off before any event until the delay has passed since start', () => {
		expect(shouldEnableFallback(T0, 0, T0 + FALLBACK_DELAY_MS - 1)).toBe(false);
		expect(shouldEnableFallback(T0, 0, T0 + FALLBACK_DELAY_MS)).toBe(true);
	});

	it('stays off while events keep arriving within the delay', () => {
		const lastEvent = T0 + FALLBACK_DELAY_MS + 1000;
		expect(shouldEnableFallback(T0, lastEvent, lastEvent + FALLBACK_DELAY_MS - 1)).toBe(false);
	});

	it('enables when the stream has been quiet for the full delay', () => {
		const lastEvent = T0 + FALLBACK_DELAY_MS + 1000;
		expect(shouldEnableFallback(T0, lastEvent, lastEvent + FALLBACK_DELAY_MS)).toBe(true);
	});

	it('measures from the most recent event, not from start', () => {
		// Started long ago, but a recent event resets the window.
		expect(shouldEnableFallback(T0, T0 + 5 * FALLBACK_DELAY_MS, T0 + 5 * FALLBACK_DELAY_MS + 1000)).toBe(false);
	});
});
