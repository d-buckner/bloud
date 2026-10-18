// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Shared test doubles for the store. Not a test file: vitest collects `*.test.ts`.
 *
 * The command layer's boundary is global `fetch`, so the fake server sits there
 * rather than behind a mock of the client modules. A test then asserts the real
 * URL, method and body that would leave the browser, which is the contract that
 * matters, and the client and `httpClient` code under test is the real code.
 */

import { vi } from 'vitest';
import { dispatch, getState } from '../kernel';
import { commandsAbandoned, homeSnapshot, settingsLoaded, toastDismissed } from '../actions';
import type { App, HomeApp } from '$lib/types';

export interface Call {
	method: string;
	url: string;
	body: unknown;
}

type Handler = (body: unknown) => Response;

/** A fetch stub keyed by `METHOD /path`. An unhandled key fails loudly. */
export class FakeServer {
	readonly calls: Call[] = [];
	private readonly routes = new Map<string, Handler>();

	on(key: string, handler: Handler): this {
		this.routes.set(key, handler);
		return this;
	}

	json(key: string, body: unknown): this {
		return this.on(key, () => jsonResponse(body));
	}

	fail(key: string, status: number, message: string): this {
		return this.on(key, () => errorResponse(status, message));
	}

	count(key: string): number {
		return this.calls.filter((call) => `${call.method} ${call.url}` === key).length;
	}

	/** Install the stub and return this, so a test can chain the routes. */
	install(): this {
		vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
			const url = typeof input === 'string' ? input : input.toString();
			const method = init?.method ?? 'GET';
			const body = init?.body === undefined ? undefined : JSON.parse(String(init.body));
			const key = `${method} ${url}`;
			this.calls.push({ method, url, body });

			const handler = this.routes.get(key);
			if (!handler) throw new Error(`no route for ${key}`);
			return handler(body);
		});
		return this;
	}
}

/** Shaped the way `httpClient.request` reads a response: text plus a content type. */
export function jsonResponse(body: unknown, status = 200): Response {
	return {
		ok: status >= 200 && status < 300,
		status,
		text: async () => JSON.stringify(body),
		json: async () => body,
		headers: new Headers({ 'content-type': 'application/json' })
	} as Response;
}

export function errorResponse(status: number, message: string): Response {
	return {
		ok: false,
		status,
		json: async () => ({ message }),
		headers: new Headers()
	} as Response;
}

/** An app record with the fields a tile and a snapshot both read. */
export function app(catalog_id: string, status: App['status'] = 'running'): HomeApp {
	return {
		id: 1,
		catalog_id,
		display_name: catalog_id,
		version: '1',
		status,
		is_system: false,
		installed_at: '2026-01-01',
		updated_at: '2026-01-01',
		x: null,
		y: null,
		w: 1,
		h: 1
	};
}

/** The home snapshot the stream would deliver. */
export function snapshot(apps: HomeApp[]): void {
	dispatch(homeSnapshot({ apps, widgets: [] }));
}

/**
 * Put the singleton back to a known state.
 *
 * The command slice is abandoned rather than patched: that is the HMR path, and a
 * test that starts clean the way a page load does is a test that cannot pass on
 * state a previous case left behind.
 */
export function resetStore(): void {
	for (const toast of getState().toasts.items) dispatch(toastDismissed({ id: toast.id }));
	dispatch(commandsAbandoned());
	dispatch(homeSnapshot({ apps: [], widgets: [] }));
	dispatch(settingsLoaded({ field: 'publicUrl', value: '', at: 0 }));
	dispatch(settingsLoaded({ field: 'timezone', value: '', at: 0 }));
	dispatch(settingsLoaded({ field: 'users', value: [], at: 0 }));
	dispatch(
		settingsLoaded({
			field: 'ai',
			value: { upstreams: [], defaultModel: '', hasApiKey: false, servedTo: [] },
			at: 0
		})
	);
	const noEndpoint = {
		configured: false,
		name: '',
		baseUrl: '',
		model: '',
		hasApiKey: false,
		servedTo: [] as string[]
	};
	dispatch(settingsLoaded({ field: 'transcription', value: noEndpoint, at: 0 }));
	dispatch(settingsLoaded({ field: 'embeddings', value: noEndpoint, at: 0 }));
}

/** The messages on screen, in order. */
export function toastMessages(): string[] {
	return getState().toasts.items.map((toast) => toast.message);
}
