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

interface CommandBase<T> {
	/** `install:jellyfin`. The dedupe key and the store key: same thing. */
	key: CommandKey;
	/** The request itself. Rejecting is a failure; a 202 is not a settle. */
	run: () => Promise<T>;
}

/** The response is the whole answer: nothing else to wait for. */
export interface RequestCommand<T> extends CommandBase<T> {
	kind: 'request';
	confirm?: undefined;
	settled?: undefined;
	within?: undefined;
}

/**
 * The write is done when the read that shows it comes back. `confirm` is
 * required, not optional: a `reread` with no read is a `request` wearing a
 * different name, which is how a save ends up reporting success for a change
 * nothing re-read.
 */
export interface RereadCommand<T> extends CommandBase<T> {
	kind: 'reread';
	confirm: () => Promise<void>;
	settled?: undefined;
	within?: undefined;
}

/**
 * The write is done when reality agrees. Both halves are required: a
 * `convergence` command with no predicate can never settle, and one with no
 * deadline can wait forever. That is a build failure rather than a runtime
 * discovery, which is the same shape as
 * `apps/configtest/waitbudget_test.go` enforces on the backend.
 */
export interface ConvergenceCommand<T> extends CommandBase<T> {
	kind: 'convergence';
	settled: (state: RootState) => boolean;
	within: number;
	confirm?: undefined;
}

/**
 * A command, as a discriminated union. The settle kind is what picks the
 * variant, so the compiler, not a test, is what refuses an incomplete one.
 */
export type CommandSpec<T> = RequestCommand<T> | RereadCommand<T> | ConvergenceCommand<T>;

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
		if (spec.kind === 'reread') await spec.confirm();
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
function awaitConvergence<T>(spec: ConvergenceCommand<T>): Promise<void> {
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
		}, spec.within);
		unsubscribe = store.subscribe(() => {
			if (spec.settled(getState())) finish(resolve);
		});
	});
}
