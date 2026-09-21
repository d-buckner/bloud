// SPDX-License-Identifier: AGPL-3.0-only
import { describe, it, expect } from 'vitest';
import { clampPercent, formatBytes, formatPercent } from '../format';

describe('formatBytes', () => {
	it('keeps whole bytes unitless of a decimal', () => {
		expect(formatBytes(512)).toBe('512 B');
		expect(formatBytes(1023)).toBe('1023 B');
	});

	it('rolls over at each 1024 step', () => {
		expect(formatBytes(1024)).toBe('1.0 KB');
		expect(formatBytes(1536)).toBe('1.5 KB');
		expect(formatBytes(1024 ** 2)).toBe('1.0 MB');
		expect(formatBytes(1024 ** 3)).toBe('1.0 GB');
		expect(formatBytes(1024 ** 4)).toBe('1.0 TB');
	});

	it('uses whole bytes and one decimal for larger units', () => {
		expect(formatBytes(900)).toBe('900 B');
		expect(formatBytes(1024 * 123.4)).toBe('123.4 KB');
	});

	it('treats absent or invalid sizes as zero rather than rendering NaN', () => {
		expect(formatBytes(0)).toBe('0 B');
		expect(formatBytes(-1)).toBe('0 B');
		expect(formatBytes(Number.NaN)).toBe('0 B');
	});

	it('clamps to the largest known unit instead of inventing one', () => {
		expect(formatBytes(1024 ** 7)).toMatch(/ PB$/);
	});
});

describe('clampPercent', () => {
	it('passes through in-range readings', () => {
		expect(clampPercent(0)).toBe(0);
		expect(clampPercent(42.6)).toBe(42.6);
		expect(clampPercent(100)).toBe(100);
	});

	it('bounds readings that would break a CSS width', () => {
		expect(clampPercent(-5)).toBe(0);
		expect(clampPercent(140)).toBe(100);
		expect(clampPercent(Number.NaN)).toBe(0);
	});
});

describe('formatPercent', () => {
	it('renders a whole-number percentage', () => {
		expect(formatPercent(42.6)).toBe('43%');
		expect(formatPercent(140)).toBe('100%');
	});
});
