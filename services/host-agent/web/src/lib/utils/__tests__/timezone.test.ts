// SPDX-License-Identifier: AGPL-3.0-only
import { describe, it, expect } from 'vitest';
import {
	FALLBACK_ZONES,
	browserZone,
	filterZones,
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

describe('filterZones', () => {
	const zones = ['America/New_York', 'America/Los_Angeles', 'Europe/London', 'Asia/Tokyo', 'Etc/UTC'];

	it('returns the head of the list for an empty query, capped', () => {
		expect(filterZones('', zones, 3)).toEqual(zones.slice(0, 3));
		expect(filterZones('   ', zones, 3)).toEqual(zones.slice(0, 3));
	});

	it('matches a city inside a zone name, case-insensitively', () => {
		expect(filterZones('york', zones)).toEqual(['America/New_York']);
		expect(filterZones('TOKYO', zones)).toEqual(['Asia/Tokyo']);
	});

	// An operator types a city, not an IANA name, so a space has to stand in for
	// the `/` and the `_` in the middle of the name.
	it('treats a space as the segment break', () => {
		expect(filterZones('new york', zones)).toEqual(['America/New_York']);
	});

	it('ranks a prefix match above a substring match', () => {
		expect(filterZones('bar', ['Foo/Bar', 'Bar/Foo'])).toEqual(['Bar/Foo', 'Foo/Bar']);
	});

	it('caps the result', () => {
		expect(filterZones('a', zones, 2)).toHaveLength(2);
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
