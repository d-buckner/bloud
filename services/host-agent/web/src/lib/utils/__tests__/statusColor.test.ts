// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { describe, expect, it } from 'vitest';
import { statusColor } from '../statusColor';

describe('statusColor', () => {
	it('maps running and active to green', () => {
		expect(statusColor('running')).toBe('#16a34a');
		expect(statusColor('active')).toBe('#16a34a');
	});

	it('maps error, exited and dead to red', () => {
		expect(statusColor('error')).toBe('#dc2626');
		expect(statusColor('exited')).toBe('#dc2626');
		expect(statusColor('dead')).toBe('#dc2626');
	});

	it('maps healthcheck to yellow', () => {
		expect(statusColor('healthcheck')).toBe('#eab308');
	});

	it('maps prestart and poststart to blue', () => {
		expect(statusColor('prestart')).toBe('#3b82f6');
		expect(statusColor('poststart')).toBe('#3b82f6');
	});

	it('maps queued and installing to the idle gray', () => {
		expect(statusColor('queued')).toBe('#9ca3af');
		expect(statusColor('installing')).toBe('#9ca3af');
	});

	it('falls back to gray for unknown and empty statuses', () => {
		expect(statusColor('bogus')).toBe('#9ca3af');
		expect(statusColor('')).toBe('#9ca3af');
	});
});
