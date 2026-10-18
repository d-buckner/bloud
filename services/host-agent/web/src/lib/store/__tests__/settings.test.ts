// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The settings slice and its views.
 *
 * The slice holds confirmed values only, and that is the property a settle
 * predicate depends on. The views are the other half of the split: they return
 * the pending claim, and they report `pending` for a write rather than for a
 * background read, so a refresh never disables a form.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { dispatch, getState } from '../kernel';
import { settingsLoaded, settingsReadFailed, settingsReadStarted } from '../actions';
import { commandStarted } from '../actions';
import { commandKey } from '../reducers/commands';
import { initialSettings } from '../reducers/settings';
import { endpointView, publicUrlView, timezoneView } from '../selectors';
import { resetStore } from './harness';
import type { AISettings, EndpointSettings } from '$lib/clients/settingsClient';

beforeEach(() => {
	resetStore();
});

describe('the settings slice', () => {
	it('starts with every resource idle and empty', () => {
		expect(initialSettings.publicUrl).toEqual({ phase: 'idle', value: null, error: '', readAt: 0 });
		expect(Object.keys(initialSettings.endpoints).sort()).toEqual(['embeddings', 'transcription']);
	});

	it('records a loaded value as confirmed', () => {
		dispatch(settingsLoaded({ field: 'publicUrl', value: 'https://bloud.example.com', at: 42 }));

		expect(getState().settings.publicUrl).toEqual({
			phase: 'ready',
			value: 'https://bloud.example.com',
			error: '',
			readAt: 42
		});
	});

	it('keeps the last good value while a read runs and when it fails', () => {
		dispatch(settingsLoaded({ field: 'timezone', value: 'Europe/Berlin', at: 1 }));

		dispatch(settingsReadStarted({ field: 'timezone' }));
		expect(getState().settings.timezone.phase).toBe('loading');
		expect(getState().settings.timezone.value).toBe('Europe/Berlin');

		dispatch(settingsReadFailed({ field: 'timezone', error: 'network down' }));
		expect(getState().settings.timezone.phase).toBe('error');
		expect(getState().settings.timezone.value).toBe('Europe/Berlin');
		expect(getState().settings.timezone.error).toBe('network down');
	});

	it('keeps the two endpoint contracts apart', () => {
		const endpoint: EndpointSettings = {
			configured: true,
			name: 'Transcription',
			baseUrl: 'https://stt.example/v1',
			model: 'whisper',
			hasApiKey: true,
			servedTo: []
		};
		dispatch(settingsLoaded({ field: 'transcription', value: endpoint, at: 1 }));

		expect(getState().settings.endpoints.transcription.value).toEqual(endpoint);
		expect(getState().settings.endpoints.embeddings.value?.configured).toBe(false);
	});
});

/** Put a pending claim on a resource without running an operation. */
function claim(key: string, optimistic: Parameters<typeof commandStarted>[0]['optimistic']): void {
	dispatch(commandStarted({ key, kind: 'convergence', at: Date.now(), optimistic }));
}

afterEach(() => {
	vi.useRealTimers();
});

describe('the settings views', () => {
	it('shows the claim over the confirmed value while a write is pending', () => {
		dispatch(settingsLoaded({ field: 'publicUrl', value: 'https://old.example', at: 1 }));
		claim(commandKey('settings', 'publicUrl'), {
			kind: 'settings.publicUrl',
			key: 'instance',
			value: { url: 'https://new.example' }
		});

		const view = publicUrlView(getState());
		expect(view.value).toBe('https://new.example');
		expect(view.confirmed).toBe('https://old.example');
		expect(view.pending).toBe(true);
	});

	it('is not pending for a read, only for a write with a claim', () => {
		dispatch(settingsReadStarted({ field: 'timezone' }));

		expect(timezoneView(getState()).pending).toBe(false);
	});

	it('stops showing a claim that confirmed state already carries', () => {
		dispatch(settingsLoaded({ field: 'timezone', value: 'Europe/Berlin', at: 1 }));
		claim(commandKey('settings', 'timezone'), {
			kind: 'settings.timezone',
			key: 'instance',
			value: { zone: 'Europe/Berlin' }
		});

		const view = timezoneView(getState());
		expect(view.value).toBe('Europe/Berlin');
		expect(view.confirmed).toBe('Europe/Berlin');
	});

	it('projects an endpoint claim per contract', () => {
		const before: EndpointSettings = {
			configured: false,
			name: 'Embeddings',
			baseUrl: '',
			model: '',
			hasApiKey: false,
			servedTo: []
		};
		const after: EndpointSettings = {
			configured: true,
			name: 'Embeddings',
			baseUrl: 'https://embed.example/v1',
			model: 'text-embedding-3-small',
			hasApiKey: false,
			servedTo: []
		};
		dispatch(settingsLoaded({ field: 'embeddings', value: before, at: 1 }));
		claim(commandKey('settings', 'embeddings'), {
			kind: 'settings.endpoint',
			key: 'embeddings',
			value: { contract: 'embeddings', settings: after }
		});

		expect(endpointView(getState(), 'embeddings').value?.baseUrl).toBe('https://embed.example/v1');
		expect(endpointView(getState(), 'transcription').value?.configured).toBe(false);
	});

	it('never writes a claim into the slice a settle predicate reads', () => {
		dispatch(settingsLoaded({ field: 'ai', value: ai('gpt'), at: 1 }));
		claim(commandKey('settings', 'ai'), {
			kind: 'settings.ai',
			key: 'instance',
			value: { settings: ai('gpt-claimed') }
		});

		expect(getState().settings.ai.value?.defaultModel).toBe('gpt');
	});

	it('returns the confirmed value once the claim is dropped', () => {
		dispatch(settingsLoaded({ field: 'publicUrl', value: 'https://confirmed.example', at: 1 }));

		expect(publicUrlView(getState()).value).toBe('https://confirmed.example');
		expect(publicUrlView(getState()).pending).toBe(false);
	});
});

function ai(model: string): AISettings {
	return {
		upstreams: [{ id: 'default', name: 'Default', baseUrl: 'https://ai.example/v1', enabled: true }],
		defaultModel: model,
		hasApiKey: false,
		servedTo: []
	};
}
