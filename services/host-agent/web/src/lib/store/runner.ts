// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The command layer - one way to ask the server for something.
 *
 * A command is not a fetch helper. It is an operation with a key, a settle kind,
 * and a record in the store while it runs, which buys three things the call sites
 * it replaces did not have:
 *
 *  - a double click is one operation, because the second call finds the key
 *    already pending and returns the first one's promise;
 *  - "saving?" is a selector over `state.commands`, not a component boolean that
 *    a second call site cannot see;
 *  - a write whose completion is not the response is representable. `convergence`
 *    is the case the two settings bugs were missing: the address save polls for
 *    convergence by hand inside a component, and the AI save treats a 202 as
 *    done and reports success for a change the orchestrator may never apply.
 *
 * There is no middleware, no thunk, no saga: this is a function that dispatches.
 */

import { dispatch, getState, store } from './kernel';
import { commandFailed, commandSettled, commandStarted } from './actions';
import type { CommandKey, SettleKind } from './reducers/commands';
import type { RootState } from './reducers';

export type { CommandKey, SettleKind };

export type CommandResult<T> = { ok: true; value: T } | { ok: false; error: string };

export interface CommandSpec<T> {
	/** `install:jellyfin`. The dedupe key and the store key: same thing. */
	key: CommandKey;
	kind: SettleKind;
	/** The request itself. Rejecting is a failure; a 202 is not a settle. */
	run: () => Promise<T>;
	/**
	 * For `reread`: the read that shows the change. Awaited before settling, so
	 * a settled `reread` means the dashboard is already looking at the new value.
	 */
	confirm?: () => Promise<void>;
	/**
	 * For `convergence`: what reality has to look like. Evaluated against the
	 * store, not against a response, because the orchestrator is the thing that
	 * has to agree.
	 */
	settled?: (state: RootState) => boolean;
	/** How long `convergence` waits before it calls the change unapplied. */
	within?: number;
}

/** Default wait for a convergence-bound command. */
export const DEFAULT_CONVERGENCE_MS = 60_000;

/** In-flight promises, so a second call joins the first instead of repeating it. */
const inFlight = new Map<CommandKey, Promise<CommandResult<unknown>>>();

/** Is anything in flight for this key right now? */
export function isRunning(key: CommandKey): boolean {
	return inFlight.has(key);
}

/**
 * Run a command. Never throws: the outcome is the return value, and the store
 * holds the record either way.
 */
export function runCommand<T>(spec: CommandSpec<T>): Promise<CommandResult<T>> {
	const existing = inFlight.get(spec.key) as Promise<CommandResult<T>> | undefined;
	if (existing) return existing;

	const running = execute(spec).finally(() => inFlight.delete(spec.key));
	inFlight.set(spec.key, running as Promise<CommandResult<unknown>>);
	return running;
}

async function execute<T>(spec: CommandSpec<T>): Promise<CommandResult<T>> {
	const at = Date.now();
	dispatch(commandStarted({ key: spec.key, kind: spec.kind, at }));

	try {
		const value = await spec.run();
		if (spec.confirm) await spec.confirm();
		if (spec.kind === 'convergence') await awaitConvergence(spec);
		dispatch(commandSettled({ key: spec.key, at: Date.now() }));
		return { ok: true, value };
	} catch (err) {
		dispatch(commandFailed({ key: spec.key, error: messageOf(err), at: Date.now() }));
		return { ok: false, error: messageOf(err) };
	}
}

function messageOf(err: unknown): string {
	if (err instanceof Error) return err.message;
	if (typeof err === 'object' && err !== null && 'message' in err) {
		return String((err as { message: unknown }).message);
	}
	return String(err);
}

/**
 * Wait for the state to show the change. Resolves when the predicate passes,
 * rejects when the deadline passes first: "accepted and never applied" is a
 * failure the UI is allowed to say out loud, and it is the one outcome a 202
 * cannot report.
 */
function awaitConvergence<T>(spec: CommandSpec<T>): Promise<void> {
	if (!spec.settled) return Promise.resolve();
	const within = spec.within ?? DEFAULT_CONVERGENCE_MS;
	if (spec.settled(getState())) return Promise.resolve();

	return new Promise<void>((resolve, reject) => {
		let done = false;
		let unsubscribe: () => void = () => undefined;

		const finish = (fn: () => void): void => {
			if (done) return;
			done = true;
			unsubscribe();
			clearTimeout(deadline);
			fn();
		};
		const deadline = setTimeout(() => {
			finish(() => reject(new Error(`${spec.key} was accepted but never applied`)));
		}, within);
		unsubscribe = store.subscribe(() => {
			if (spec.settled && spec.settled(getState())) finish(resolve);
		});
	});
}
