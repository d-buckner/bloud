// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { statusColor } from '../statusColor';

describe('statusColor', () => {
	it('maps running and active to green', () => {
		expect(statusColor('running')).toBe('#16a34a');
		expect(statusColor('active')).toBe('#16a34a');
	});

	it('maps error, exited, dead and failed to red', () => {
		expect(statusColor('error')).toBe('#dc2626');
		expect(statusColor('exited')).toBe('#dc2626');
		expect(statusColor('dead')).toBe('#dc2626');
		expect(statusColor('failed')).toBe('#dc2626');
	});

	it('maps healthcheck and starting to yellow', () => {
		expect(statusColor('healthcheck')).toBe('#eab308');
		expect(statusColor('starting')).toBe('#eab308');
	});

	it('maps the config phases to blue', () => {
		expect(statusColor('prestart')).toBe('#3b82f6');
		expect(statusColor('poststart')).toBe('#3b82f6');
		expect(statusColor('configuring')).toBe('#3b82f6');
		expect(statusColor('finalizing')).toBe('#3b82f6');
	});

	it('maps queued and installing to the idle gray', () => {
		expect(statusColor('queued')).toBe('#9ca3af');
		expect(statusColor('installing')).toBe('#9ca3af');
	});

	// The AI Model node reads "external" and must not read as alive. Keeping
	// it out of the color table on purpose is what makes the dot the same
	// neutral gray as every other unprobed status, so pin it: adding
	// "external" to STATUS_COLORS would silently turn the node green or blue
	// and imply a liveness nothing verifies.
	it('keeps external and catalog on the neutral gray', () => {
		expect(statusColor('external')).toBe('#9ca3af');
		expect(statusColor('catalog')).toBe('#9ca3af');
	});

	it('falls back to gray for unknown and empty statuses', () => {
		expect(statusColor('bogus')).toBe('#9ca3af');
		expect(statusColor('')).toBe('#9ca3af');
	});
});
