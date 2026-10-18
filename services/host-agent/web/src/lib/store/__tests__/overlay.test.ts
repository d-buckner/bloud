// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The overlay algebra.
 *
 * Two functions per kind, tested apart: what a claim shows, and when confirmed
 * state already carries it. The second one is the one that matters. A command
 * settles on it, so a wrong `satisfied` is either a save that never settles or a
 * save that settles on its own claim and flickers.
 *
 * These are pure functions over a value, so there is no store and no timer here.
 * The lifecycle tests own that.
 */

import { describe, expect, it } from 'vitest';
import { overlayReady, project, satisfied } from '../overlay';
import type { AppsState } from '../reducers/apps';
import type { AISettings, EndpointSettings } from '$lib/clients/settingsClient';
import type { ManagedUser } from '$lib/clients/userClient';
import { app } from './harness';
import { createStore } from 'redux';
import { rootReducer } from '../reducers';
import { homeSnapshot, settingsLoaded } from '../actions';

function appsState(...apps: ReturnType<typeof app>[]): AppsState {
	return {
		byId: Object.fromEntries(apps.map((a) => [a.catalog_id, a])),
		order: apps.map((a) => a.catalog_id),
		loading: false,
		error: null,
		snapshotAt: 1
	};
}

const EMPTY_APPS: AppsState = { byId: {}, order: [], loading: false, error: null, snapshotAt: 1 };

const AI: AISettings = {
	upstreams: [{ id: 'default', name: 'Default', baseUrl: 'https://ai.example/v1', enabled: true }],
	defaultModel: 'gpt',
	hasApiKey: true,
	servedTo: [{ app: 'hermes', via: 'gateway' }]
};

const ENDPOINT: EndpointSettings = {
	configured: true,
	name: 'Transcription',
	baseUrl: 'https://stt.example/v1',
	model: 'whisper',
	hasApiKey: true,
	servedTo: ['podly']
};

const USERS: ManagedUser[] = [
	{ id: 1, username: 'ada', name: 'Ada', is_admin: true, is_active: true },
	{ id: 2, username: 'bob', name: 'Bob', is_admin: false, is_active: true }
];

describe('apps.record', () => {
	const claim = { record: app('jellyfin', 'installing') };

	it('adds an app the snapshot does not know about yet', () => {
		const shown = project('apps.record', claim, EMPTY_APPS);

		expect(shown.byId['jellyfin']).toEqual(claim.record);
		expect(shown.order).toEqual(['jellyfin']);
	});

	it('is satisfied as soon as the snapshot carries the app', () => {
		const confirmed = appsState(app('jellyfin', 'running'));

		expect(satisfied('apps.record', claim, confirmed)).toBe(true);
		// The confirmed record wins over the 202's copy: it is newer.
		expect(project('apps.record', claim, confirmed)).toBe(confirmed);
	});

	it('does not add a system or headless filter of its own', () => {
		// The grid filters with `isGridApp`; the overlay must not duplicate that
		// rule, or an app could be hidden by two different definitions.
		const headless = { ...app('hermes', 'installing'), headless: true };
		const shown = project('apps.record', { record: headless }, EMPTY_APPS);

		expect(shown.byId['hermes']).toBeDefined();
	});
});

describe('apps.patch', () => {
	const running = appsState(app('immich', 'running'));

	it('merges the claimed fields over the confirmed record', () => {
		const shown = project(
			'apps.patch',
			{ catalogId: 'immich', patch: { status: 'uninstalling' } },
			running
		);

		expect(shown.byId['immich'].status).toBe('uninstalling');
		expect(shown.byId['immich'].display_name).toBe('immich');
	});

	it('is satisfied when every claimed field already matches', () => {
		expect(satisfied('apps.patch', { catalogId: 'immich', patch: { status: 'running' } }, running)).toBe(
			true
		);
		expect(
			satisfied('apps.patch', { catalogId: 'immich', patch: { status: 'uninstalling' } }, running)
		).toBe(false);
	});

	it('is satisfied when the app is gone, which is what an uninstall means', () => {
		expect(
			satisfied('apps.patch', { catalogId: 'immich', patch: { status: 'uninstalling' } }, EMPTY_APPS)
		).toBe(true);
	});

	it('patches nothing when the app is absent', () => {
		expect(project('apps.patch', { catalogId: 'nope', patch: { status: 'error' } }, EMPTY_APPS)).toBe(
			EMPTY_APPS
		);
	});

	it('compares more than status, so a rename settles on the name', () => {
		const claim = { catalogId: 'immich', patch: { display_name: 'Photos' } };

		expect(satisfied('apps.patch', claim, running)).toBe(false);
		expect(satisfied('apps.patch', claim, appsState(app('immich', 'running')))).toBe(false);
		expect(
			satisfied('apps.patch', claim, {
				...running,
				byId: { ...running.byId, immich: { ...running.byId.immich, display_name: 'Photos' } }
			})
		).toBe(true);
	});
});

describe('apps.removed', () => {
	const claim = { catalogId: 'immich' };

	it('hides the app while the claim is pending', () => {
		const shown = project('apps.removed', claim, appsState(app('immich'), app('jellyfin')));

		expect(shown.byId['immich']).toBeUndefined();
		expect(shown.order).toEqual(['jellyfin']);
	});

	it('is satisfied only when the record is gone', () => {
		expect(satisfied('apps.removed', claim, appsState(app('immich')))).toBe(false);
		expect(satisfied('apps.removed', claim, EMPTY_APPS)).toBe(true);
	});

	it('is a no-op when the app is not there', () => {
		expect(project('apps.removed', claim, EMPTY_APPS)).toBe(EMPTY_APPS);
	});
});

describe('overlayReady, the settle rule', () => {
	it('asks confirmed state, not the projection', () => {
		const store = createStore(rootReducer);
		store.dispatch(
			settingsLoaded({ field: 'publicUrl', value: 'https://confirmed.example', at: 1 })
		);

		const claim = { kind: 'settings.publicUrl', key: 'instance', value: { url: 'https://wanted.example' } } as const;
		expect(overlayReady(claim, store.getState())).toBe(false);

		store.dispatch(
			settingsLoaded({ field: 'publicUrl', value: 'https://wanted.example', at: 2 })
		);
		expect(overlayReady(claim, store.getState())).toBe(true);
	});

	it('waits for a removed app to leave the snapshot', () => {
		const store = createStore(rootReducer);
		store.dispatch(homeSnapshot({ apps: [app('immich')], widgets: [] }));

		const claim = { kind: 'apps.removed', key: 'immich', value: { catalogId: 'immich' } } as const;
		expect(overlayReady(claim, store.getState())).toBe(false);

		store.dispatch(homeSnapshot({ apps: [], widgets: [] }));
		expect(overlayReady(claim, store.getState())).toBe(true);
	});
});

describe('settings.publicUrl and settings.timezone', () => {
	it('shows the claim until the confirmed value equals it', () => {
		expect(project('settings.publicUrl', { url: 'https://new.example' }, 'https://old.example')).toBe(
			'https://new.example'
		);
		expect(satisfied('settings.publicUrl', { url: 'https://new.example' }, 'https://new.example')).toBe(
			true
		);
		expect(satisfied('settings.publicUrl', { url: 'https://new.example' }, 'https://old.example')).toBe(
			false
		);
	});

	it('treats an empty time zone as a value, not as missing', () => {
		expect(project('settings.timezone', { zone: '' }, 'Europe/Berlin')).toBe('');
		expect(satisfied('settings.timezone', { zone: '' }, '')).toBe(true);
	});
});

describe('settings.ai', () => {
	const claim = { settings: AI };

	it('is satisfied by the fields the save controls', () => {
		expect(satisfied('settings.ai', claim, AI)).toBe(true);
		expect(satisfied('settings.ai', claim, { ...AI, defaultModel: 'other' })).toBe(false);
		expect(satisfied('settings.ai', claim, { ...AI, hasApiKey: false })).toBe(false);
		expect(
			satisfied('settings.ai', claim, {
				...AI,
				upstreams: [{ id: 'default', name: 'Default', baseUrl: 'https://other/v1', enabled: true }]
			})
		).toBe(false);
	});

	it('ignores served-to, which is the server answer and not ours to claim', () => {
		expect(satisfied('settings.ai', claim, { ...AI, servedTo: [] })).toBe(true);
	});
});

describe('settings.endpoint', () => {
	const claim = { contract: 'transcription' as const, settings: ENDPOINT };

	it('compares the five fields the API echoes back', () => {
		expect(satisfied('settings.endpoint', claim, ENDPOINT)).toBe(true);
		expect(satisfied('settings.endpoint', claim, { ...ENDPOINT, configured: false })).toBe(false);
		expect(satisfied('settings.endpoint', claim, { ...ENDPOINT, model: 'other' })).toBe(false);
		expect(satisfied('settings.endpoint', claim, null)).toBe(false);
	});
});

describe('settings.users', () => {
	it('matches regardless of order', () => {
		expect(satisfied('settings.users', { users: [USERS[1], USERS[0]] }, USERS)).toBe(true);
	});

	it('does not match a different role or a missing member', () => {
		expect(
			satisfied('settings.users', { users: [{ ...USERS[0], is_admin: false }, USERS[1]] }, USERS)
		).toBe(false);
		expect(satisfied('settings.users', { users: [USERS[0]] }, USERS)).toBe(false);
	});
});
