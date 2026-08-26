// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { afterEach, describe, expect, it } from 'vitest';
import { getAppUrl, getRemoteAppUrl } from '../appUrl';

// Minimal window.location stub (tests run in a node environment without a DOM).
function setLocation(hostname: string, port = '8080', protocol = 'http:') {
	(globalThis as { window?: unknown }).window = {
		location: { hostname, port, protocol, href: `${protocol}//${hostname}` },
	};
}

afterEach(() => {
	setLocation('localhost');
});

describe('getAppUrl', () => {
	it('uses localhost as-is in dev', () => {
		setLocation('localhost');
		expect(getAppUrl('jellyfin')).toBe('http://jellyfin.localhost:8080');
	});

	it('uses port 80 URLs as-is (no suffix)', () => {
		setLocation('localhost', '');
		expect(getAppUrl('jellyfin')).toBe('http://jellyfin.localhost');
	});

	it('uses bloud.local as-is', () => {
		setLocation('bloud.local', '');
		expect(getAppUrl('immich')).toBe('http://immich.bloud.local');
	});

	it('uses a custom domain as-is (no bloud. stripping)', () => {
		setLocation('bloud.example.com', '');
		expect(getAppUrl('affine')).toBe('http://affine.bloud.example.com');
	});

	it('strips bloud. only for Tailscale MagicDNS domains', () => {
		setLocation('bloud.mytailnet.ts.net', '');
		expect(getAppUrl('navidrome')).toBe('http://navidrome.mytailnet.ts.net');
	});

	it('appends paths with a leading slash', () => {
		setLocation('bloud.local', '');
		expect(getAppUrl('jellyfin', 'settings')).toBe('http://jellyfin.bloud.local/settings');
	});
});

describe('getRemoteAppUrl', () => {
	it('builds slug subdomains on the current host', () => {
		setLocation('bloud.local', '');
		expect(getRemoteAppUrl('jellyfin', 'Johan')).toBe('http://jellyfin-johan.bloud.local');
	});
});
