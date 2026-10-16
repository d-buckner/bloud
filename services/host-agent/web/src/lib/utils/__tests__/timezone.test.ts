// SPDX-License-Identifier: AGPL-3.0-only
import { describe, it, expect } from 'vitest';
import {
	FALLBACK_ZONES,
	browserZone,
	offsetLabel,
	supportedZones,
	zoneOptions
} from '../timezone';

describe('supportedZones', () => {
	it('returns a non-empty list of IANA names', () => {
		const zones = supportedZones();
		expect(zones.length).toBeGreaterThan(0);
		expect(zones).toContain('Europe/Berlin');
	});
});

describe('browserZone', () => {
	it('returns a zone name or an empty string, never a throw', () => {
		expect(typeof browserZone()).toBe('string');
	});
});

describe('zoneOptions', () => {
	it('keeps the base order and appends a name the base list lacks', () => {
		const options = zoneOptions(['Etc/UTC', 'Europe/Berlin'], ['Asia/Tokyo']);
		expect(options).toEqual(['Etc/UTC', 'Europe/Berlin', 'Asia/Tokyo']);
	});

	// A saved zone the browser does not list must stay selectable. If it were
	// dropped, the select would show its first option and a Save would change a
	// zone the operator never touched.
	it('keeps a saved zone the browser does not list', () => {
		const options = zoneOptions(FALLBACK_ZONES, ['Antarctica/Troll']);
		expect(options).toContain('Antarctica/Troll');
	});

	it('drops duplicates and empty names', () => {
		const options = zoneOptions(['Europe/Berlin'], ['Europe/Berlin', '', 'Asia/Tokyo']);
		expect(options).toEqual(['Europe/Berlin', 'Asia/Tokyo']);
	});
});

describe('offsetLabel', () => {
	const winter = new Date('2026-01-01T12:00:00Z');
	const summer = new Date('2026-07-01T12:00:00Z');

	it('renders the offset with the UTC prefix the zone list uses', () => {
		expect(offsetLabel('Etc/UTC', winter)).toBe('UTC+0');
		expect(offsetLabel('Asia/Kolkata', winter)).toBe('UTC+5:30');
	});

	// The line is what catches a wrong pick, so it has to follow the zone across
	// the year, not just name its winter offset.
	it('follows the zone across a daylight-saving change', () => {
		expect(offsetLabel('Europe/Berlin', winter)).toBe('UTC+1');
		expect(offsetLabel('Europe/Berlin', summer)).toBe('UTC+2');
	});

	it('is empty for no zone', () => {
		expect(offsetLabel('', winter)).toBe('');
	});

	// A zone the runtime does not know must not take the form down: the preview
	// line is a convenience, and a missing one is better than an error boundary.
	it('is empty for a zone the runtime cannot render', () => {
		expect(offsetLabel('Mars/Olympus', winter)).toBe('');
	});
});
