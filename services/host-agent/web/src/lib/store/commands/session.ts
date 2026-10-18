// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Session commands - find out who is looking, then open the stream.
 *
 * Replaces the `onMount` body in `+layout.svelte`, which held four local
 * booleans (`loading`, `setupRequired`, `user`, plus an implicit "we gave up")
 * and swallowed every error as "dev mode". The same three outcomes are state
 * now, and the redirect is one branch instead of a comment.
 */

import { get } from '$lib/clients/httpClient';
import type { Role } from '$lib/types';
import { runCommand } from '../runner';
import { commandKey } from '../reducers/commands';
import { bootstrapLoaded, bootstrapStarted, connectionEvent, signedOut } from '../actions';
import { dispatch } from '../kernel';
import { startTransport, stopTransport } from '../effects/connection';

interface SetupStatus {
	setupRequired: boolean;
	authentikReady: boolean;
}

interface AuthMe {
	id: number;
	username: string;
	role: Role;
}

const BOOTSTRAP_KEY = commandKey('bootstrap', 'session');

/**
 * Read setup state and identity, then let the connection machine open the
 * stream. Safe to call twice: the command layer dedupes on the key.
 */
export function bootstrapSession(): Promise<void> {
	return runCommand({ key: BOOTSTRAP_KEY, run: readSession }).then(() => undefined);
}

async function readSession(): Promise<void> {
	dispatch(bootstrapStarted());
	const setup = await get<SetupStatus>('/api/setup/status');
	if (setup.setupRequired) {
		dispatch(bootstrapLoaded({ user: null, setupRequired: true }));
		return;
	}

	const res = await fetch('/api/auth/me', { headers: { Accept: 'application/json' } });
	if (!res.ok) {
		// Anonymous, and the only way forward is the identity provider.
		dispatch(bootstrapLoaded({ user: null, setupRequired: false }));
		window.location.href = '/auth/login';
		return;
	}

	const me = (await res.json()) as AuthMe;
	dispatch(bootstrapLoaded({ user: { username: me.username, role: me.role ?? 'admin' }, setupRequired: false }));
	// The session is what the stream is authenticated against: no session, no stream.
	dispatch(connectionEvent({ type: 'boot.done' }));
}

/** Tear the transport down before handing the browser to the logout route. */
export function signOut(): void {
	dispatch(signedOut());
	stopTransport();
}

/** Boot the dashboard: watch the phase, then read the session. */
export function startDashboard(): void {
	startTransport();
	void bootstrapSession();
}
