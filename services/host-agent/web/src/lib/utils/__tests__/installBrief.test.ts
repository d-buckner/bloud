// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import type { CatalogApp } from '$lib/types';
import { installBrief, type BriefContext } from '$lib/utils/installBrief';

/**
 * The install brief is the copy a person reads before they commit to a download,
 * so what matters about it is what it refuses to claim. These tests pin the
 * claims: an app that is not installed is never described as connected, a system
 * app nobody chose is never named, and a section with nothing true to say is
 * absent rather than empty.
 */

function app(over: Partial<CatalogApp> = {}): CatalogApp {
	return {
		catalogId: 'podly',
		displayName: 'Podly',
		description: 'Podcast manager',
		category: 'media',
		...over
	};
}

function context(over: Partial<BriefContext> = {}): BriefContext {
	return {
		isInstalled: () => false,
		isUserApp: (name) => name !== 'traefik' && name !== 'authentik',
		displayName: (name) => name.charAt(0).toUpperCase() + name.slice(1),
		...over
	};
}

function lines(brief: ReturnType<typeof installBrief>, title: string): string[] {
	return brief.find((s) => s.title === title)?.lines ?? [];
}

describe('sign-in section', () => {
	it('names the strategy a native-oidc app gets', () => {
		const brief = installBrief(app({ sso: { strategy: 'native-oidc' } }), context());
		expect(lines(brief, 'Sign-in')[0]).toContain('signs in with your Bloud account');
	});

	it('says when Bloud also creates the app account', () => {
		const brief = installBrief(
			app({ sso: { strategy: 'native-oidc', userCreation: true } }),
			context()
		);
		expect(lines(brief, 'Sign-in')).toContain(
			'Bloud creates a Podly account for each Bloud user at first sign-in.'
		);
	});

	it('says forward-auth checks the session at the door', () => {
		const brief = installBrief(app({ sso: { strategy: 'forward-auth' } }), context());
		expect(lines(brief, 'Sign-in')[0]).toContain('checks your Bloud session');
	});

	it('says an app with no strategy keeps its own credential', () => {
		const brief = installBrief(app(), context());
		expect(lines(brief, 'Sign-in')[0]).toContain('keeps its own account and password');
	});
});

describe('connections section', () => {
	const mediaServer = {
		mediaServer: { required: false, multi: false, compatible: [{ app: 'jellyfin', default: true }] }
	};

	it('names a provider that is already installed', () => {
		const brief = installBrief(
			app({ integrations: mediaServer }),
			context({ isInstalled: (n) => n === 'jellyfin' })
		);
		expect(lines(brief, 'Connections')).toEqual(['Connects to Jellyfin (media server).']);
	});

	it('names the default a required contract installs when nothing is there', () => {
		const brief = installBrief(
			app({
				integrations: {
					proxy: { required: true, multi: false, compatible: [{ app: 'traefik', default: true }] },
					mediaServer: { required: true, multi: false, compatible: [{ app: 'jellyfin', default: true }] }
				}
			}),
			context()
		);
		expect(lines(brief, 'Connections')).toEqual([
			'Installs Jellyfin with it, then connects (media server).'
		]);
	});

	it('offers an optional provider rather than promising one', () => {
		const brief = installBrief(app({ integrations: mediaServer }), context());
		expect(lines(brief, 'Connections')[0]).toContain('if you install one');
	});

	it('drops a contract whose only provider is system infrastructure', () => {
		const brief = installBrief(
			app({
				integrations: {
					proxy: { required: true, multi: false, compatible: [{ app: 'traefik', default: true }] }
				}
			}),
			context()
		);
		expect(lines(brief, 'Connections')).toEqual([]);
		expect(brief.map((s) => s.title)).not.toContain('Connections');
	});

	it('lists every installed provider of a multi contract', () => {
		const brief = installBrief(
			app({
				integrations: {
					pvr: {
						required: false,
						multi: true,
						compatible: [{ app: 'sonarr' }, { app: 'radarr' }]
					}
				}
			}),
			context({ isInstalled: (n) => n === 'sonarr' || n === 'radarr' })
		);
		expect(lines(brief, 'Connections')).toEqual(['Connects to Sonarr and Radarr (media library manager).']);
	});
});

describe('machine section', () => {
	it('states the download estimate the card states', () => {
		const brief = installBrief(app({ estimatedSizeMB: 1500 }), context());
		expect(lines(brief, 'On this machine')).toContain('~1.5 GB to download.');
	});

	it('says nothing about size when the app declares none', () => {
		const brief = installBrief(app(), context());
		expect(lines(brief, 'On this machine')).toEqual([]);
	});

	it('counts containers only when there is more than one', () => {
		const brief = installBrief(
			app({ containers: [{ name: 'a' }, { name: 'b' }] }),
			context()
		);
		expect(lines(brief, 'On this machine')).toContain('Runs 2 containers.');
	});

	it('names the apps an install brings along', () => {
		const brief = installBrief(
			app({
				integrations: {
					mediaServer: { required: true, multi: false, compatible: [{ app: 'jellyfin', default: true }] }
				}
			}),
			context()
		);
		expect(lines(brief, 'On this machine')).toContain('Also installs Jellyfin.');
	});
});
