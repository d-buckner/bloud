// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The time zone form's pure logic: which zones to offer, and how to show one.
 *
 * Kept out of the component so the boundary cases (a browser without
 * `Intl.supportedValuesOf`, a saved zone the browser does not list, a zone the
 * runtime cannot render) are unit-testable.
 */

/**
 * The list offered when the browser cannot answer. It is short on purpose: the
 * server accepts any name the zone database knows, so this only has to be usable
 * until `Intl.supportedValuesOf` exists, which every current browser has.
 */
export const FALLBACK_ZONES = [
	'Etc/UTC',
	'Africa/Cairo',
	'America/Bogota',
	'America/Chicago',
	'America/Denver',
	'America/Los_Angeles',
	'America/Mexico_City',
	'America/New_York',
	'America/Sao_Paulo',
	'Asia/Bangkok',
	'Asia/Dubai',
	'Asia/Jakarta',
	'Asia/Kolkata',
	'Asia/Manila',
	'Asia/Shanghai',
	'Asia/Tokyo',
	'Australia/Sydney',
	'Europe/Berlin',
	'Europe/Lisbon',
	'Europe/London',
	'Europe/Madrid',
	'Europe/Moscow',
	'Europe/Paris',
	'Pacific/Auckland'
];

/**
 * The zones the browser knows. A browser that has no list, or throws when asked,
 * gets the fallback rather than an empty picker.
 */
export function supportedZones(): string[] {
	const withList = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
	if (typeof withList.supportedValuesOf === 'function') {
		try {
			const list = withList.supportedValuesOf('timeZone');
			if (list.length > 0) return list;
		} catch {
			// A browser that throws here is handled by the fallback below.
		}
	}
	return FALLBACK_ZONES;
}

/** The zone the browser itself runs in, or "" when it will not say. */
export function browserZone(): string {
	try {
		return Intl.DateTimeFormat().resolvedOptions().timeZone ?? '';
	} catch {
		return '';
	}
}

/**
 * The zones to render, in order, with any `extra` names the base list lacks
 * appended. A saved zone the browser does not list has to stay selectable, or the
 * select shows its first option and a Save changes a zone nobody touched. They
 * go last so the browser's own alphabetical order stays readable.
 */
export function zoneOptions(base: string[], extra: string[]): string[] {
	const seen: string[] = [];
	for (const zone of [...base, ...extra]) {
		if (zone && !seen.includes(zone)) seen.push(zone);
	}
	return seen;
}

/**
 * The zone's offset from UTC, rendered as `UTC+1` or `UTC-5:30`, or "" when the
 * zone is empty or the runtime cannot render it.
 *
 * The form shows an offset rather than a wall clock: a clock that is not re-ticked
 * becomes a wrong number the operator has to catch, and an offset is the same
 * fact without the decay. It is what catches the wrong pick, which is the only
 * job the line has.
 */
export function offsetLabel(zone: string, at: Date = new Date()): string {
	if (!zone) return '';
	try {
		const parts = new Intl.DateTimeFormat('en-US', {
			timeZone: zone,
			timeZoneName: 'shortOffset'
		}).formatToParts(at);
		const name = parts.find((part) => part.type === 'timeZoneName')?.value ?? '';
		// The runtime says GMT; the catalog's own names say UTC, and the form
		// should read like the list it came from.
		return name.replace(/^GMT/, 'UTC');
	} catch {
		// A runtime without `shortOffset`, or a zone it does not know, gets no
		// line. The preview is a convenience, not a gate on saving.
		return '';
	}
}
