// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Settings operations, end to end through the public `commands` object.
 *
 * The engine is tested on its own in `commandLifecycle.test.ts`. This file tests
 * the wiring: which route is called, what the claim looks like, and whether the
 * confirmed read that ends the wait is the one the operation declared. The HTTP
 * boundary is `fetch`, so the real clients and `httpClient` run.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { commands } from '../commands';
import { dispatch, getState, store } from '../kernel';
import { aiView, endpointView, gridApps, publicUrlView, usersView } from '../selectors';
import { settingsLoaded } from '../actions';
import type { AISettings, EndpointSettings } from '$lib/clients/settingsClient';
import type { ManagedUser } from '$lib/clients/userClient';
import { FakeServer, app, resetStore, snapshot, toastMessages } from './harness';

let server: FakeServer;

beforeEach(() => {
	resetStore();
	vi.useFakeTimers();
	server = new FakeServer().install();
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

describe('the address', () => {
	it('loads the confirmed value into the slice', async () => {
		server.json('GET /api/settings/public-url', { url: 'https://bloud.example.com' });

		await commands.loadPublicURL();

		expect(getState().settings.publicUrl.value).toBe('https://bloud.example.com');
		expect(getState().settings.publicUrl.phase).toBe('ready');
	});

	it('shows the typed address, then the canonical one the server stored', async () => {
		server.json('PUT /api/settings/public-url', { intentId: '7', url: 'http://bloud.example.com' });
		server.json('GET /api/settings/public-url', { url: 'http://bloud.example.com' });

		const promise = commands.savePublicURL('bloud.example.com');
		expect(publicUrlView(getState()).value).toBe('bloud.example.com');

		await vi.advanceTimersByTimeAsync(0);
		expect(publicUrlView(getState()).value).toBe('http://bloud.example.com');
		expect(server.calls[0]).toEqual({
			method: 'PUT',
			url: '/api/settings/public-url',
			body: { url: 'bloud.example.com' }
		});

		await vi.advanceTimersByTimeAsync(500);
		expect(await promise).toEqual({ ok: true, value: { intentId: '7', url: 'http://bloud.example.com' } });
		expect(publicUrlView(getState()).pending).toBe(false);
		expect(publicUrlView(getState()).value).toBe('http://bloud.example.com');
	});

	it('keeps the claim while the instance has not applied the change, then reports the cap', async () => {
		server.json('PUT /api/settings/public-url', { intentId: '8', url: 'https://never.example' });
		server.json('GET /api/settings/public-url', { url: 'https://old.example' });

		const promise = commands.savePublicURL('https://never.example');
		await vi.advanceTimersByTimeAsync(0);
		expect(publicUrlView(getState()).value).toBe('https://never.example');

		// The poll keeps the claim for the whole window rather than snapping back.
		await vi.advanceTimersByTimeAsync(30_000);
		expect(publicUrlView(getState()).value).toBe('https://never.example');

		// Collect toasts as they arrive: a toast lives 5s, and the advance past the
		// cap also runs that dismissal timer.
		const seen: string[] = [];
		const stop = store.subscribe(() => {
			for (const message of toastMessages()) if (!seen.includes(message)) seen.push(message);
		});

		await vi.advanceTimersByTimeAsync(600_000);
		stop();
		const result = await promise;

		expect(result.ok).toBe(false);
		expect(publicUrlView(getState()).value).toBe('https://old.example');
		expect(seen).toEqual(['The new address is not live yet']);
	});
});

describe('the time zone', () => {
	it('treats an empty zone as a value rather than a missing one', async () => {
		dispatch(settingsLoaded({ field: 'timezone', value: 'Europe/Berlin', at: 1 }));
		server.json('PUT /api/settings/timezone', { intentId: '9', zone: '' });
		server.json('GET /api/settings/timezone', { zone: '' });

		const promise = commands.saveTimezone('');
		await vi.advanceTimersByTimeAsync(0);
		expect(server.calls[0].body).toEqual({ zone: '' });

		await vi.advanceTimersByTimeAsync(500);
		expect(await promise).toEqual({ ok: true, value: { intentId: '9', zone: '' } });
		expect(getState().settings.timezone.value).toBe('');
	});
});

describe('the AI settings', () => {
	const stored: AISettings = {
		upstreams: [{ id: 'default', name: 'Default', baseUrl: 'https://ai.example/v1', enabled: true }],
		defaultModel: 'gpt',
		hasApiKey: true,
		servedTo: [{ app: 'hermes', via: 'gateway' }]
	};

	it('keeps the stored key and the served-to list in the claim', async () => {
		dispatch(settingsLoaded({ field: 'ai', value: stored, at: 1 }));
		server.json('PUT /api/settings/ai', { intentId: '10' });
		server.json('GET /api/settings/ai', {
			...stored,
			defaultModel: 'gpt-next',
			upstreams: [{ ...stored.upstreams[0], baseUrl: 'https://ai2.example/v1' }]
		});

		const promise = commands.saveAISettings({
			upstreams: [{ ...stored.upstreams[0], baseUrl: 'https://ai2.example/v1' }],
			defaultModel: 'gpt-next'
		});

		expect(aiView(getState()).value).toEqual({
			upstreams: [{ ...stored.upstreams[0], baseUrl: 'https://ai2.example/v1' }],
			defaultModel: 'gpt-next',
			hasApiKey: true,
			servedTo: [{ app: 'hermes', via: 'gateway' }]
		});

		await vi.advanceTimersByTimeAsync(500);
		expect((await promise).ok).toBe(true);
		expect(getState().settings.ai.value?.defaultModel).toBe('gpt-next');
	});

	it('reports a typed key as present before the server answers', async () => {
		dispatch(settingsLoaded({ field: 'ai', value: { ...stored, hasApiKey: false }, at: 1 }));
		server.json('PUT /api/settings/ai', { intentId: '11' });
		server.json('GET /api/settings/ai', { ...stored, hasApiKey: true });

		const promise = commands.saveAISettings({
			upstreams: stored.upstreams,
			defaultModel: 'gpt',
			apiKey: 'sk-typed'
		});

		expect(aiView(getState()).value?.hasApiKey).toBe(true);

		await vi.advanceTimersByTimeAsync(500);
		await promise;
	});
});

describe('a single-endpoint capability', () => {
	const stored: EndpointSettings = {
		configured: true,
		name: 'Transcription',
		baseUrl: 'https://old.example/v1',
		model: 'whisper',
		hasApiKey: false,
		servedTo: []
	};

	it('adopts the stored fields the response reports', async () => {
		dispatch(settingsLoaded({ field: 'transcription', value: stored, at: 1 }));
		server.json('PUT /api/settings/transcription', {
			status: 'ok',
			baseUrl: 'https://new.example/v1',
			model: 'whisper-large',
			configured: true
		});
		server.json('GET /api/settings/transcription', {
			...stored,
			baseUrl: 'https://new.example/v1',
			model: 'whisper-large'
		});

		const promise = commands.saveEndpoint('transcription', {
			baseUrl: 'new.example/v1',
			name: 'Transcription',
			model: 'whisper-large'
		});

		await vi.advanceTimersByTimeAsync(0);
		expect(endpointView(getState(), 'transcription').value?.baseUrl).toBe('https://new.example/v1');

		await vi.advanceTimersByTimeAsync(500);
		expect((await promise).ok).toBe(true);
	});

	it('removes the provider with an empty address', async () => {
		dispatch(settingsLoaded({ field: 'transcription', value: stored, at: 1 }));
		server.json('PUT /api/settings/transcription', {
			status: 'ok',
			baseUrl: '',
			model: '',
			configured: false
		});
		server.json('GET /api/settings/transcription', {
			configured: false,
			name: 'Transcription',
			baseUrl: '',
			model: '',
			hasApiKey: false,
			servedTo: []
		});

		const promise = commands.removeEndpoint('transcription');
		await vi.advanceTimersByTimeAsync(0);
		expect(server.calls[0].body).toEqual({ baseUrl: '', name: 'Transcription', model: '' });
		expect(endpointView(getState(), 'transcription').value?.configured).toBe(false);

		await vi.advanceTimersByTimeAsync(500);
		expect((await promise).ok).toBe(true);
	});
});

describe('the user list', () => {
	const ada: ManagedUser = { id: 1, username: 'ada', name: 'Ada', is_admin: true, is_active: true };
	const bob: ManagedUser = { id: 2, username: 'bob', name: 'Bob', is_admin: false, is_active: true };

	it('shows a created member before the list is re-read', async () => {
		dispatch(settingsLoaded({ field: 'users', value: [ada], at: 1 }));
		server.json('POST /api/admin/users', null);
		server.json('GET /api/admin/users', { users: [ada, bob] });

		const promise = commands.createUser({ username: 'bob', password: 'secret', role: 'member' });

		expect(usersView(getState()).value?.map((user) => user.username)).toEqual(['ada', 'bob']);
		expect(usersView(getState()).value?.[1].is_admin).toBe(false);

		await vi.advanceTimersByTimeAsync(500);
		expect((await promise).ok).toBe(true);
		expect(usersView(getState()).pending).toBe(false);
	});

	it('drops a deleted member at once', async () => {
		dispatch(settingsLoaded({ field: 'users', value: [ada, bob], at: 1 }));
		server.json('DELETE /api/admin/users/bob', null);
		server.json('GET /api/admin/users', { users: [ada] });

		const promise = commands.deleteUser('bob');
		expect(usersView(getState()).value?.map((user) => user.username)).toEqual(['ada']);

		await vi.advanceTimersByTimeAsync(500);
		expect((await promise).ok).toBe(true);
	});

	it('flips a role at once and confirms it', async () => {
		dispatch(settingsLoaded({ field: 'users', value: [ada, bob], at: 1 }));
		server.json('PUT /api/admin/users/bob/role', null);
		server.json('GET /api/admin/users', {
			users: [ada, { ...bob, is_admin: true }]
		});

		const promise = commands.setUserRole('bob', 'admin');
		expect(usersView(getState()).value?.[1].is_admin).toBe(true);

		await vi.advanceTimersByTimeAsync(500);
		expect((await promise).ok).toBe(true);
		expect(getState().settings.users.value?.[1].is_admin).toBe(true);
	});
});

describe('the app commands', () => {
	it('keeps an installed tile past the 202 until the snapshot carries it', async () => {
		server.json('POST /api/apps/jellyfin/install', {
			intentId: '12',
			app: app('jellyfin', 'installing')
		});

		const promise = commands.installApp('jellyfin');
		await vi.advanceTimersByTimeAsync(0);
		expect(gridApps(getState()).map((entry) => entry.catalog_id)).toEqual(['jellyfin']);

		// A snapshot that has not caught up must not remove the tile the user just got.
		snapshot([]);
		expect(gridApps(getState()).map((entry) => entry.catalog_id)).toEqual(['jellyfin']);

		snapshot([app('jellyfin', 'installing')]);
		await vi.advanceTimersByTimeAsync(1_000);

		expect((await promise).ok).toBe(true);
		expect(getState().commands.byKey['install:jellyfin'].status).toBe('settled');
		expect(gridApps(getState()).map((entry) => entry.catalog_id)).toEqual(['jellyfin']);
	});

	it('removes an uninstalled tile at once and settles when the app is gone', async () => {
		snapshot([app('immich', 'running')]);
		server.json('POST /api/apps/immich/uninstall', { intentId: '13' });

		const promise = commands.uninstallApp('immich');
		await vi.advanceTimersByTimeAsync(0);
		expect(gridApps(getState())).toEqual([]);
		// The record is still confirmed; only the grid rule hides it.
		expect(getState().apps.byId['immich']).toBeDefined();

		snapshot([]);
		await vi.advanceTimersByTimeAsync(1_000);

		expect((await promise).ok).toBe(true);
		expect(getState().apps.byId['immich']).toBeUndefined();
	});

	it('renames before the pass that stores it', async () => {
		snapshot([app('immich', 'running')]);
		server.json('PATCH /api/apps/immich/rename', { intentId: '14' });

		const promise = commands.renameApp('immich', 'Photos');
		await vi.advanceTimersByTimeAsync(0);
		expect(gridApps(getState())[0].display_name).toBe('Photos');

		snapshot([{ ...app('immich', 'running'), display_name: 'Photos' }]);
		await vi.advanceTimersByTimeAsync(1_000);

		expect((await promise).ok).toBe(true);
		expect(getState().commands.byKey['rename:immich'].status).toBe('settled');
	});

	it('raises the operation words when the server refuses', async () => {
		server.fail('POST /api/apps/jellyfin/install', 500, 'catalog is stale');

		await commands.installApp('jellyfin');

		expect(toastMessages()).toEqual(['Could not install jellyfin']);
		expect(gridApps(getState())).toEqual([]);
	});
});
