// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { statusColor } from '../statusColor';

/**
 * The mapping is asserted against the design system's tokens rather than against
 * hex values on purpose: the tokens are the contrast decision (every one of them
 * clears 7:1 on white, where the colors this replaced did not), and pinning the
 * hex here would make a deliberate palette change read as a broken test.
 */
describe('statusColor', () => {
	it('maps running and active to the success token', () => {
		expect(statusColor('running')).toBe('var(--color-success)');
		expect(statusColor('active')).toBe('var(--color-success)');
	});

	it('maps error, exited, dead and failed to the error token', () => {
		expect(statusColor('error')).toBe('var(--color-error)');
		expect(statusColor('exited')).toBe('var(--color-error)');
		expect(statusColor('dead')).toBe('var(--color-error)');
		expect(statusColor('failed')).toBe('var(--color-error)');
	});

	it('maps healthcheck and starting to the warning token', () => {
		expect(statusColor('healthcheck')).toBe('var(--color-warning)');
		expect(statusColor('starting')).toBe('var(--color-warning)');
	});

	it('maps the config phases to the info token', () => {
		expect(statusColor('prestart')).toBe('var(--color-info)');
		expect(statusColor('poststart')).toBe('var(--color-info)');
		expect(statusColor('configuring')).toBe('var(--color-info)');
		expect(statusColor('finalizing')).toBe('var(--color-info)');
	});

	it('maps queued and installing to the idle gray', () => {
		expect(statusColor('queued')).toBe('var(--color-text-secondary)');
		expect(statusColor('installing')).toBe('var(--color-text-secondary)');
	});

	// The AI Model node reads "external" and an app the catalog knows but has
	// never installed reads "catalog". Nothing reconciles either one, so neither
	// sends a phase, and both read as settled: the same green a running node gets.
	// The hollow ring they used to draw read as a state that went missing.
	it('maps external and catalog to the success token', () => {
		expect(statusColor('external')).toBe('var(--color-success)');
		expect(statusColor('catalog')).toBe('var(--color-success)');
	});

	it('falls back to gray for unknown and empty statuses', () => {
		expect(statusColor('bogus')).toBe('var(--color-text-secondary)');
		expect(statusColor('')).toBe('var(--color-text-secondary)');
	});
});
